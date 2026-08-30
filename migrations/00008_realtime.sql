-- +goose Up
ALTER TABLE chat.conversation_members
 ADD COLUMN last_read_sequence bigint NOT NULL DEFAULT 0 CHECK(last_read_sequence>=0),
 ADD COLUMN last_delivered_sequence bigint NOT NULL DEFAULT 0 CHECK(last_delivered_sequence>=last_read_sequence);
GRANT UPDATE(last_read_sequence,last_delivered_sequence) ON chat.conversation_members TO alur_runtime;
-- The Foundation last_seen_at column is now updated by Redis heartbeat batches.
GRANT UPDATE(last_seen_at) ON chat.users TO alur_runtime;

-- +goose Down
ALTER TABLE chat.conversation_members DROP COLUMN last_read_sequence,DROP COLUMN last_delivered_sequence;
