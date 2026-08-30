-- +goose Up
-- External Auth proves identity only. Existing users receive no implicit admin grant.
ALTER TABLE chat.users ADD COLUMN role text NOT NULL DEFAULT 'user' CHECK(role IN ('user','admin'));
-- Remove broad grants before restoring only the columns required by runtime workflows.
-- The HTTP role cannot insert/update the permission column, even through raw SQL.
REVOKE INSERT, UPDATE ON chat.users FROM alur_runtime;
GRANT INSERT(id,project_id,external_user_id,display_name) ON chat.users TO alur_runtime;
GRANT UPDATE(external_user_id,display_name,avatar_url,updated_at) ON chat.users TO alur_runtime;

-- +goose Down
ALTER TABLE chat.users DROP COLUMN role;
GRANT INSERT,UPDATE ON chat.users TO alur_runtime;
