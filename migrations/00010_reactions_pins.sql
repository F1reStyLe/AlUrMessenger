-- +goose Up
-- Relations repeat Project and conversation so accidental cross-tenant references
-- are rejected by PostgreSQL as well as the application authorization boundary.
CREATE TABLE chat.message_reactions (
 project_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 message_id uuid NOT NULL,
 user_id uuid NOT NULL,
 reaction text NOT NULL CHECK(octet_length(reaction) BETWEEN 1 AND 128),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(project_id,message_id,user_id,reaction),
 FOREIGN KEY(project_id,conversation_id,message_id) REFERENCES chat.messages(project_id,conversation_id,id),
 FOREIGN KEY(project_id,user_id) REFERENCES chat.users(project_id,id)
);
CREATE TABLE chat.pinned_messages (
 project_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 message_id uuid NOT NULL,
 pinned_by uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(project_id,conversation_id,message_id),
 FOREIGN KEY(project_id,conversation_id,message_id) REFERENCES chat.messages(project_id,conversation_id,id),
 FOREIGN KEY(project_id,pinned_by) REFERENCES chat.users(project_id,id)
);
-- An association is added or removed, never reassigned to another actor/message.
REVOKE UPDATE ON chat.message_reactions,chat.pinned_messages FROM alur_runtime;

-- +goose Down
DROP TABLE chat.pinned_messages;
DROP TABLE chat.message_reactions;
