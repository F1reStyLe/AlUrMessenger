-- +goose Up
-- Minimal conversation aggregate. Message counters/references arrive with Messages.
CREATE TABLE chat.conversations (
 id uuid PRIMARY KEY,
 project_id uuid NOT NULL REFERENCES chat.projects(id),
 type text NOT NULL CHECK(type IN ('DIRECT','GROUP','CHANNEL')),
 title text NOT NULL DEFAULT '' CHECK(char_length(title)<=128),
 avatar_url text NOT NULL DEFAULT '' CHECK(char_length(avatar_url)<=2048),
 created_by uuid NOT NULL,
 resource_version bigint NOT NULL DEFAULT 1 CHECK(resource_version>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 deleted_at timestamptz,
 UNIQUE(project_id,id),
 UNIQUE(project_id,id,type),
 FOREIGN KEY(project_id,created_by) REFERENCES chat.users(project_id,id),
 CHECK(type<>'DIRECT' OR (title='' AND avatar_url=''))
);
CREATE INDEX conversations_project_cursor ON chat.conversations(project_id,id) WHERE deleted_at IS NULL;

-- The canonical UUID ordering and tenant-qualified PK survive retries and races.
-- The literal type makes it impossible to attach a pair to a GROUP/CHANNEL.
CREATE TABLE chat.direct_pairs (
 project_id uuid NOT NULL,
 low_user_id uuid NOT NULL,
 high_user_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 conversation_type text NOT NULL DEFAULT 'DIRECT' CHECK(conversation_type='DIRECT'),
 PRIMARY KEY(project_id,low_user_id,high_user_id),
 UNIQUE(project_id,conversation_id),
 CHECK(low_user_id<high_user_id),
 FOREIGN KEY(project_id,low_user_id) REFERENCES chat.users(project_id,id),
 FOREIGN KEY(project_id,high_user_id) REFERENCES chat.users(project_id,id),
 FOREIGN KEY(project_id,conversation_id,conversation_type) REFERENCES chat.conversations(project_id,id,type)
);

-- Initial membership is needed now to keep conversation reads private. Public
-- membership/role management, checkpoints and mute are separate implementation steps.
CREATE TABLE chat.conversation_members (
 project_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 user_id uuid NOT NULL,
 role text NOT NULL CHECK(role IN ('member','moderator')),
 joined_at timestamptz NOT NULL DEFAULT now(),
 left_at timestamptz,
 banned boolean NOT NULL DEFAULT false,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 PRIMARY KEY(project_id,conversation_id,user_id),
 FOREIGN KEY(project_id,conversation_id) REFERENCES chat.conversations(project_id,id),
 FOREIGN KEY(project_id,user_id) REFERENCES chat.users(project_id,id)
);
CREATE INDEX members_user_cursor ON chat.conversation_members(project_id,user_id,conversation_id) WHERE left_at IS NULL;

-- A third DIRECT member, role escalation or leave must fail even through raw SQL.
-- Pair rows and conversation identity are immutable for the runtime account.
-- +goose StatementBegin
CREATE FUNCTION chat.guard_direct_member() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM chat.conversations WHERE project_id=NEW.project_id AND id=NEW.conversation_id AND type='DIRECT') THEN
  IF NEW.role<>'member' OR NEW.left_at IS NOT NULL OR NOT EXISTS(
   SELECT 1 FROM chat.direct_pairs WHERE project_id=NEW.project_id AND conversation_id=NEW.conversation_id
   AND NEW.user_id IN (low_user_id,high_user_id)
  ) THEN
   RAISE EXCEPTION 'DIRECT_MEMBER_INVALID' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER guard_direct_member BEFORE INSERT OR UPDATE ON chat.conversation_members
 FOR EACH ROW EXECUTE FUNCTION chat.guard_direct_member();

-- Validate the completed creation transaction, not its intermediate INSERT order.
-- No partially created DIRECT or GROUP/CHANNEL without creator moderator may commit.
-- +goose StatementBegin
CREATE FUNCTION chat.check_conversation_creation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.type='DIRECT' THEN
  IF (SELECT count(*) FROM chat.conversation_members WHERE project_id=NEW.project_id AND conversation_id=NEW.id)<>2
   OR NOT EXISTS(SELECT 1 FROM chat.direct_pairs WHERE project_id=NEW.project_id AND conversation_id=NEW.id AND NEW.created_by IN (low_user_id,high_user_id)) THEN
   RAISE EXCEPTION 'DIRECT_PAIR_INCOMPLETE' USING ERRCODE='23514';
  END IF;
 ELSIF NOT EXISTS(SELECT 1 FROM chat.conversation_members WHERE project_id=NEW.project_id AND conversation_id=NEW.id AND user_id=NEW.created_by AND role='moderator' AND left_at IS NULL AND NOT banned) THEN
  RAISE EXCEPTION 'CREATOR_MEMBERSHIP_REQUIRED' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER check_conversation_creation AFTER INSERT ON chat.conversations
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION chat.check_conversation_creation();

REVOKE UPDATE,DELETE ON chat.conversations FROM alur_runtime;
GRANT UPDATE(title,avatar_url,resource_version,updated_at) ON chat.conversations TO alur_runtime;
REVOKE UPDATE,DELETE ON chat.direct_pairs FROM alur_runtime;
REVOKE UPDATE,DELETE ON chat.conversation_members FROM alur_runtime;
-- Row locking needs UPDATE on one column; role/leave management is not exposed yet.
GRANT UPDATE(version) ON chat.conversation_members TO alur_runtime;

-- +goose Down
DROP TRIGGER check_conversation_creation ON chat.conversations;
DROP FUNCTION chat.check_conversation_creation();
DROP TABLE chat.conversation_members;
DROP FUNCTION chat.guard_direct_member();
DROP TABLE chat.direct_pairs;
DROP TABLE chat.conversations;
