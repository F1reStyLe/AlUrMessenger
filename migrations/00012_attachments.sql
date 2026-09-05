-- +goose Up
-- Upload metadata is committed before the object write. A crash can therefore
-- leave an explicit uploading row for the Phase 6.3 sweeper, never an invisible
-- MinIO object whose owner/Project cannot be recovered.
CREATE TABLE chat.storage_objects (
 id uuid PRIMARY KEY,
 project_id uuid NOT NULL REFERENCES chat.projects(id),
 storage_key text NOT NULL UNIQUE CHECK(octet_length(storage_key) BETWEEN 1 AND 512),
 mime_type text NOT NULL CHECK(mime_type IN ('image/jpeg','image/png','image/webp')),
 size bigint NOT NULL CHECK(size BETWEEN 1 AND 52428800),
 sha256 bytea NOT NULL CHECK(octet_length(sha256)=32),
 status text NOT NULL CHECK(status IN ('uploading','ready','pending_delete','deleted')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(project_id,id)
);
CREATE INDEX storage_objects_cleanup ON chat.storage_objects(status,created_at);

CREATE TABLE chat.attachments (
 id uuid PRIMARY KEY,
 project_id uuid NOT NULL,
 uploader_id uuid NOT NULL,
 object_id uuid NOT NULL,
 original_name text NOT NULL CHECK(octet_length(original_name) BETWEEN 1 AND 255),
 mime_type text NOT NULL CHECK(mime_type IN ('image/jpeg','image/png','image/webp')),
 size bigint NOT NULL CHECK(size BETWEEN 1 AND 52428800),
 width integer NOT NULL CHECK(width BETWEEN 1 AND 8192),
 height integer NOT NULL CHECK(height BETWEEN 1 AND 8192),
 status text NOT NULL CHECK(status IN ('uploading','ready','attached','deleted')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL DEFAULT (clock_timestamp()+interval '24 hours'),
 UNIQUE(project_id,id),
 UNIQUE(project_id,object_id),
 FOREIGN KEY(project_id,uploader_id) REFERENCES chat.users(project_id,id),
 FOREIGN KEY(project_id,object_id) REFERENCES chat.storage_objects(project_id,id)
);
CREATE INDEX attachments_uploader ON chat.attachments(project_id,uploader_id,created_at DESC);
CREATE INDEX attachments_cleanup ON chat.attachments(status,expires_at);

-- Runtime creates rows and performs only explicit lifecycle transitions. It
-- cannot delete evidence or rewrite ownership/content identity.
REVOKE DELETE ON chat.storage_objects,chat.attachments FROM alur_runtime;
REVOKE UPDATE ON chat.storage_objects,chat.attachments FROM alur_runtime;
GRANT UPDATE(status,updated_at) ON chat.storage_objects TO alur_runtime;
GRANT UPDATE(status) ON chat.attachments TO alur_runtime;

-- +goose Down
DROP TABLE chat.attachments;
DROP TABLE chat.storage_objects;
