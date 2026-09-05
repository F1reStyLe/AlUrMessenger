-- +goose Up
ALTER TABLE chat.project_settings
 ADD COLUMN blacklist_enabled boolean NOT NULL DEFAULT false,
 ADD COLUMN blacklist_policy text NOT NULL DEFAULT 'reject' CHECK(blacklist_policy='reject');

CREATE TABLE chat.blacklist_entries (
 id uuid PRIMARY KEY,
 project_id uuid NOT NULL REFERENCES chat.projects(id),
 normalized_word text NOT NULL CHECK(octet_length(normalized_word) BETWEEN 1 AND 256),
 enabled boolean NOT NULL DEFAULT true,
 resource_version bigint NOT NULL DEFAULT 1 CHECK(resource_version>0),
 created_by uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(project_id,id),
 UNIQUE(project_id,normalized_word),
 FOREIGN KEY(project_id,created_by) REFERENCES chat.users(project_id,id)
);
CREATE INDEX blacklist_project_cursor ON chat.blacklist_entries(project_id,id);
GRANT UPDATE(enabled,resource_version,updated_at) ON chat.blacklist_entries TO alur_runtime;

-- +goose Down
DROP TABLE chat.blacklist_entries;
ALTER TABLE chat.project_settings DROP COLUMN blacklist_policy,DROP COLUMN blacklist_enabled;
