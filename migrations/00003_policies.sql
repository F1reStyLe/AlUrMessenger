-- +goose Up
CREATE TABLE chat.project_settings (
 project_id uuid PRIMARY KEY REFERENCES chat.projects(id),
 flags jsonb NOT NULL DEFAULT '{"allow_images":true,"allow_reactions":true,"allow_edit":true,"allow_delete":true,"allow_forward":true,"allow_reply":true,"allow_pin":true,"typing_enabled":true,"presence_enabled":true,"read_receipts":true,"allow_bots":false,"allow_webhooks":false}'::jsonb,
 max_upload_size bigint NOT NULL DEFAULT 10485760 CHECK(max_upload_size BETWEEN 1 AND 52428800),
 message_retention_days integer NOT NULL DEFAULT 365 CHECK(message_retention_days BETWEEN 1 AND 3650),
 settings_version bigint NOT NULL DEFAULT 1 CHECK(settings_version>0),
 updated_by uuid,
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(project_id,updated_by) REFERENCES chat.users(project_id,id),
 CHECK(jsonb_typeof(flags)='object')
);
INSERT INTO chat.project_settings(project_id) SELECT id FROM chat.projects;
REVOKE INSERT, DELETE ON chat.project_settings FROM alur_runtime;
GRANT UPDATE(policy_version) ON chat.projects TO alur_runtime;

CREATE TABLE chat.audit_logs (
 id uuid PRIMARY KEY,
 project_id uuid NOT NULL REFERENCES chat.projects(id),
 actor_id uuid NOT NULL,
 actor_type text NOT NULL CHECK(actor_type='user'),
 action text NOT NULL,
 resource_type text NOT NULL,
 resource_id uuid NOT NULL,
 metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
 ip inet,
 request_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(project_id,actor_id) REFERENCES chat.users(project_id,id)
);
CREATE INDEX audit_project_cursor ON chat.audit_logs(project_id,id);
-- Append-only for application credentials; database operators remain trusted.
REVOKE UPDATE, DELETE ON chat.audit_logs FROM alur_runtime;

-- +goose Down
DROP TABLE chat.audit_logs;
REVOKE UPDATE(policy_version) ON chat.projects FROM alur_runtime;
DROP TABLE chat.project_settings;
