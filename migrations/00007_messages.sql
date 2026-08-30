-- +goose Up
ALTER TABLE chat.conversations ADD COLUMN message_sequence bigint NOT NULL DEFAULT 0 CHECK(message_sequence>=0);
ALTER TABLE chat.conversations ADD COLUMN event_sequence bigint NOT NULL DEFAULT 0 CHECK(event_sequence>=0);
ALTER TABLE chat.conversations ADD COLUMN last_message_id uuid;
GRANT UPDATE(message_sequence,event_sequence,last_message_id) ON chat.conversations TO alur_runtime;

-- Only the payload is encrypted; routing headers and lifecycle timestamps remain visible.
CREATE TABLE chat.messages (
 id uuid PRIMARY KEY,
 project_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 sender_id uuid NOT NULL,
 type text NOT NULL CHECK(type IN ('TEXT','SYSTEM')),
 encrypted_content bytea NOT NULL,
 nonce bytea NOT NULL CHECK(octet_length(nonce)=12),
 key_version text NOT NULL,
 payload_version integer NOT NULL CHECK(payload_version=1),
 sequence bigint NOT NULL CHECK(sequence>0),
 resource_version bigint NOT NULL DEFAULT 1 CHECK(resource_version>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL,
 deleted_at timestamptz,
 UNIQUE(project_id,id),
 UNIQUE(project_id,conversation_id,id),
 UNIQUE(project_id,conversation_id,sequence),
 FOREIGN KEY(project_id,conversation_id) REFERENCES chat.conversations(project_id,id),
 FOREIGN KEY(project_id,sender_id) REFERENCES chat.users(project_id,id),
 CHECK(expires_at>created_at)
);
CREATE INDEX messages_history ON chat.messages(project_id,conversation_id,sequence DESC);
CREATE INDEX messages_expiration ON chat.messages(expires_at,id);
ALTER TABLE chat.conversations ADD CONSTRAINT conversations_last_message_fk
 FOREIGN KEY(project_id,id,last_message_id) REFERENCES chat.messages(project_id,conversation_id,id);

CREATE TABLE chat.message_idempotency (
 project_id uuid NOT NULL,
 sender_id uuid NOT NULL,
 client_message_id uuid NOT NULL,
 message_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 fingerprint bytea NOT NULL CHECK(octet_length(fingerprint)=32),
 fingerprint_version text NOT NULL,
 expires_at timestamptz NOT NULL,
 PRIMARY KEY(project_id,sender_id,client_message_id),
 FOREIGN KEY(project_id,sender_id) REFERENCES chat.users(project_id,id),
 FOREIGN KEY(project_id,conversation_id,message_id) REFERENCES chat.messages(project_id,conversation_id,id)
);
CREATE INDEX idempotency_expiration ON chat.message_idempotency(expires_at);

CREATE TABLE chat.message_search (
 project_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 message_id uuid NOT NULL,
 search_key_version text NOT NULL,
 tokens text[] NOT NULL,
 PRIMARY KEY(project_id,message_id,search_key_version),
 FOREIGN KEY(project_id,conversation_id,message_id) REFERENCES chat.messages(project_id,conversation_id,id)
);
CREATE INDEX message_search_tokens ON chat.message_search USING gin(tokens);
CREATE INDEX message_search_conversation ON chat.message_search(project_id,conversation_id);

-- Event log and outbox contain references only, never plaintext/ciphertext content.
CREATE TABLE chat.conversation_events (
 id uuid PRIMARY KEY,
 project_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 sequence bigint NOT NULL CHECK(sequence>0),
 envelope jsonb NOT NULL,
 occurred_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(project_id,conversation_id,sequence),
 UNIQUE(project_id,conversation_id,sequence,id),
 FOREIGN KEY(project_id,conversation_id) REFERENCES chat.conversations(project_id,id)
);
CREATE TABLE chat.outbox_events (
 event_id uuid PRIMARY KEY REFERENCES chat.conversation_events(id),
 project_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 sequence bigint NOT NULL,
 envelope jsonb NOT NULL,
 attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 published_at timestamptz,
 last_error_code text,
 FOREIGN KEY(project_id,conversation_id,sequence,event_id) REFERENCES chat.conversation_events(project_id,conversation_id,sequence,id)
);
CREATE INDEX outbox_head ON chat.outbox_events(project_id,conversation_id,sequence) WHERE published_at IS NULL;
CREATE INDEX outbox_ready ON chat.outbox_events(next_attempt_at) WHERE published_at IS NULL;
REVOKE UPDATE,DELETE ON chat.messages,chat.conversation_events FROM alur_runtime;
REVOKE DELETE ON chat.outbox_events FROM alur_runtime;
REVOKE UPDATE ON chat.outbox_events FROM alur_runtime;
GRANT UPDATE(attempts,next_attempt_at,published_at,last_error_code) ON chat.outbox_events TO alur_runtime;

-- Logical consumer deduplication survives Kafka rebalances and process restarts.
CREATE TABLE chat.consumer_inbox (
 consumer text NOT NULL,
 event_id uuid NOT NULL,
 processed_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(consumer,event_id)
);
REVOKE UPDATE,DELETE ON chat.consumer_inbox FROM alur_runtime;

-- +goose Down
DROP TABLE chat.consumer_inbox;
DROP TABLE chat.outbox_events;
DROP TABLE chat.conversation_events;
DROP TABLE chat.message_search;
DROP TABLE chat.message_idempotency;
ALTER TABLE chat.conversations DROP CONSTRAINT conversations_last_message_fk;
DROP TABLE chat.messages;
ALTER TABLE chat.conversations DROP COLUMN last_message_id,DROP COLUMN message_sequence,DROP COLUMN event_sequence;
