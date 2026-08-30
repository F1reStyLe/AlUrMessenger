-- +goose Up
-- Project binding is operator-managed; runtime may read, never provision or retarget an issuer.
CREATE TABLE chat.projects (
 id uuid PRIMARY KEY,
 name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 128),
 status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
 auth_issuer text NOT NULL CHECK (length(auth_issuer)>0),
 auth_audience text NOT NULL CHECK (length(auth_audience)>0),
 policy_version bigint NOT NULL DEFAULT 1 CHECK (policy_version>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
REVOKE INSERT, UPDATE, DELETE ON chat.projects FROM alur_runtime;
-- Row locks require UPDATE on one column. Auth bindings remain operator-only.
GRANT UPDATE(updated_at) ON chat.projects TO alur_runtime;
CREATE TABLE chat.users (
 id uuid PRIMARY KEY,
 project_id uuid NOT NULL REFERENCES chat.projects(id),
 external_user_id text,
 kind text NOT NULL DEFAULT 'human' CHECK (kind IN ('human','bot','system')),
 display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 128),
 avatar_url text NOT NULL DEFAULT '',
 status text NOT NULL DEFAULT 'offline',
 last_seen_at timestamptz,
 banned_at timestamptz,
 ban_reason text NOT NULL DEFAULT '',
 policy_version bigint NOT NULL DEFAULT 1 CHECK (policy_version>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(project_id,id),
 UNIQUE(project_id,external_user_id),
 CHECK (external_user_id IS NULL OR char_length(external_user_id) BETWEEN 1 AND 256),
 CHECK (kind <> 'human' OR external_user_id IS NOT NULL)
);
CREATE INDEX users_project_cursor ON chat.users(project_id,id);

-- +goose Down
DROP TABLE chat.users;
DROP TABLE chat.projects;
