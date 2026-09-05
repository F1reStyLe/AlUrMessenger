-- +goose Up
-- The header retains immediate lineage for lifecycle work. The independently
-- encrypted payload retains content and safe author attribution after source purge.
ALTER TABLE chat.messages ADD COLUMN forwarded_from_message_id uuid;
ALTER TABLE chat.messages ADD CONSTRAINT messages_forward_source_fk
 FOREIGN KEY(project_id,forwarded_from_message_id)
 REFERENCES chat.messages(project_id,id) ON DELETE SET NULL (forwarded_from_message_id);
ALTER TABLE chat.messages ADD CONSTRAINT messages_forward_not_self
 CHECK(forwarded_from_message_id IS DISTINCT FROM id);
CREATE INDEX messages_forward_source ON chat.messages(project_id,forwarded_from_message_id)
 WHERE forwarded_from_message_id IS NOT NULL;

-- +goose Down
DROP INDEX chat.messages_forward_source;
ALTER TABLE chat.messages DROP CONSTRAINT messages_forward_not_self;
ALTER TABLE chat.messages DROP CONSTRAINT messages_forward_source_fk;
ALTER TABLE chat.messages DROP COLUMN forwarded_from_message_id;
