-- +goose Up
-- Reply references cannot cross either Project or conversation boundaries.
-- Published migrations remain immutable; existing encrypted payloads need no rewrite.
ALTER TABLE chat.messages ADD COLUMN reply_to_message_id uuid;
ALTER TABLE chat.messages ADD COLUMN edited_at timestamptz;
ALTER TABLE chat.messages ADD CONSTRAINT messages_reply_fk
 FOREIGN KEY(project_id,conversation_id,reply_to_message_id)
 REFERENCES chat.messages(project_id,conversation_id,id);
ALTER TABLE chat.messages ADD CONSTRAINT messages_reply_not_self CHECK(reply_to_message_id IS DISTINCT FROM id);
CREATE INDEX messages_reply ON chat.messages(project_id,conversation_id,reply_to_message_id) WHERE reply_to_message_id IS NOT NULL;

-- Runtime can replace/redact payloads but cannot change identity, ordering or TTL,
-- physically delete a message, or rewrite durable events and dedup fingerprints.
GRANT UPDATE(encrypted_content,nonce,key_version,payload_version,resource_version,edited_at,deleted_at,reply_to_message_id)
 ON chat.messages TO alur_runtime;

-- +goose Down
REVOKE UPDATE(encrypted_content,nonce,key_version,payload_version,resource_version,edited_at,deleted_at,reply_to_message_id)
 ON chat.messages FROM alur_runtime;
ALTER TABLE chat.messages DROP CONSTRAINT messages_reply_fk;
ALTER TABLE chat.messages DROP CONSTRAINT messages_reply_not_self;
DROP INDEX chat.messages_reply;
ALTER TABLE chat.messages DROP COLUMN reply_to_message_id,DROP COLUMN edited_at;
