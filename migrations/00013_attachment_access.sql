-- +goose Up
-- A logical attachment belongs to at most one message. IMAGE forward creates a
-- new logical attachment, so source/target can share an object safely.
ALTER TABLE chat.messages DROP CONSTRAINT messages_type_check;
ALTER TABLE chat.messages ADD CONSTRAINT messages_type_check CHECK(type IN ('TEXT','IMAGE','SYSTEM'));
ALTER TABLE chat.attachments DROP CONSTRAINT attachments_project_id_object_id_key;

CREATE TABLE chat.message_attachments (
 project_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 message_id uuid NOT NULL,
 attachment_id uuid NOT NULL,
 position smallint NOT NULL DEFAULT 0 CHECK(position=0),
 PRIMARY KEY(project_id,message_id,attachment_id),
 UNIQUE(project_id,attachment_id),
 FOREIGN KEY(project_id,conversation_id,message_id) REFERENCES chat.messages(project_id,conversation_id,id),
 FOREIGN KEY(project_id,attachment_id) REFERENCES chat.attachments(project_id,id)
);
REVOKE UPDATE ON chat.message_attachments FROM alur_runtime;

-- +goose Down
DROP TABLE chat.message_attachments;
ALTER TABLE chat.attachments ADD CONSTRAINT attachments_project_id_object_id_key UNIQUE(project_id,object_id);
ALTER TABLE chat.messages DROP CONSTRAINT messages_type_check;
ALTER TABLE chat.messages ADD CONSTRAINT messages_type_check CHECK(type IN ('TEXT','SYSTEM'));
