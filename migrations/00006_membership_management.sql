-- +goose Up
ALTER TABLE chat.conversation_members ADD COLUMN muted boolean NOT NULL DEFAULT false;
-- Identity columns remain immutable; the repository serializes all membership
-- mutations on the conversation row and checks the last active moderator.
GRANT UPDATE(role,left_at,banned,muted) ON chat.conversation_members TO alur_runtime;

-- +goose Down
REVOKE UPDATE(role,left_at,banned,muted) ON chat.conversation_members FROM alur_runtime;
ALTER TABLE chat.conversation_members DROP COLUMN muted;
