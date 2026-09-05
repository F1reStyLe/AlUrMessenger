-- +goose Up
-- Global Project bans are moderation state. Runtime may change only the ban
-- envelope and optimistic policy version; identity/role remain operator-owned.
GRANT UPDATE(banned_at,ban_reason,policy_version) ON chat.users TO alur_runtime;

-- +goose Down
REVOKE UPDATE(banned_at,ban_reason,policy_version) ON chat.users FROM alur_runtime;
