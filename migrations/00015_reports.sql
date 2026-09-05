-- +goose Up
CREATE TABLE chat.reports (
 id uuid PRIMARY KEY,
 project_id uuid NOT NULL REFERENCES chat.projects(id),
 reporter_id uuid NOT NULL,
 target_user_id uuid,
 message_id uuid,
 reported_message_id uuid,
 conversation_id uuid,
 reason text NOT NULL CHECK(reason IN ('spam','harassment','abuse','impersonation','other')),
 encrypted_description bytea NOT NULL,
 nonce bytea NOT NULL CHECK(octet_length(nonce)=12),
 key_version text NOT NULL CHECK(octet_length(key_version) BETWEEN 1 AND 32),
 payload_version smallint NOT NULL DEFAULT 1 CHECK(payload_version=1),
 status text NOT NULL DEFAULT 'OPEN' CHECK(status IN ('OPEN','REVIEWING','RESOLVED','REJECTED')),
 resource_version bigint NOT NULL DEFAULT 1 CHECK(resource_version>0),
 reviewed_by uuid,
 reviewed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(project_id,id),
 FOREIGN KEY(project_id,reporter_id) REFERENCES chat.users(project_id,id),
 FOREIGN KEY(project_id,target_user_id) REFERENCES chat.users(project_id,id),
 FOREIGN KEY(project_id,conversation_id,message_id) REFERENCES chat.messages(project_id,conversation_id,id) ON DELETE SET NULL (message_id),
 FOREIGN KEY(project_id,reviewed_by) REFERENCES chat.users(project_id,id),
 CHECK((reported_message_id IS NOT NULL AND target_user_id IS NULL AND conversation_id IS NOT NULL) OR
       (reported_message_id IS NULL AND message_id IS NULL AND target_user_id IS NOT NULL)),
 CHECK((status IN ('OPEN','REVIEWING') AND reviewed_by IS NULL AND reviewed_at IS NULL) OR
       (status IN ('RESOLVED','REJECTED') AND reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL))
);
CREATE INDEX reports_project_status_cursor ON chat.reports(project_id,status,id);
CREATE INDEX reports_reporter_cursor ON chat.reports(project_id,reporter_id,id);
REVOKE UPDATE,DELETE ON chat.reports FROM alur_runtime;
GRANT UPDATE(status,resource_version,reviewed_by,reviewed_at,updated_at) ON chat.reports TO alur_runtime;

-- +goose Down
DROP TABLE chat.reports;
