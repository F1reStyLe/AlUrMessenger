// Package repository persists conversations with tenant-qualified SQL and atomic membership.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	eventrepo "github.com/F1reStyLe/AlUrMessenger/internal/event/repository"
	"sort"

	"github.com/F1reStyLe/AlUrMessenger/internal/auth"
	"github.com/F1reStyLe/AlUrMessenger/internal/conversation"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store has only runtime credentials; no schema/provisioning privileges are needed.
type Store struct{ DB *pgxpool.Pool }

// querier allows the exact same authorized projection inside or outside a transaction.
type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// projection intentionally includes active membership and Project status in SQL.
// A UUID or global admin role alone can never reveal a private conversation.
const projection = `SELECT c.id::text,c.type,c.title,c.avatar_url,c.created_by::text,c.created_at,c.updated_at,c.resource_version,
 m.user_id::text,m.role,m.joined_at,m.banned,m.version,m.last_read_sequence,m.last_delivered_sequence,c.message_sequence,c.event_sequence,c.last_message_id::text
 FROM chat.conversations c
 JOIN chat.projects p ON p.id=c.project_id AND p.status='active'
 JOIN chat.conversation_members m ON m.project_id=c.project_id AND m.conversation_id=c.id
 WHERE c.project_id=$1 AND m.user_id=$2 AND m.left_at IS NULL AND c.deleted_at IS NULL`

// scan centralizes response shape and hides absent/foreign/nonmember resources as 404.
func scan(row pgx.Row) (conversation.Conversation, error) {
	var c conversation.Conversation
	err := row.Scan(&c.ID, &c.Type, &c.Title, &c.AvatarURL, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt, &c.Version, &c.Membership.UserID, &c.Membership.Role, &c.Membership.JoinedAt, &c.Membership.Banned, &c.Membership.Version, &c.Membership.Read, &c.Membership.Delivered, &c.MessageSequence, &c.EventSequence, &c.LastMessageID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = identity.ErrNotFound
	}
	return c, err
}

// get is shared by creation, standalone reads and locked mutations.
func get(ctx context.Context, q querier, a identity.Actor, id string, locked bool) (conversation.Conversation, error) {
	query := projection + " AND c.id=$3"
	if locked {
		query += " FOR SHARE OF m"
	}
	return scan(q.QueryRow(ctx, query, a.ProjectID, a.User.ID, id))
}

// Get does not trust cached membership or an Actor.Admin flag.
func (s *Store) Get(ctx context.Context, a identity.Actor, id string) (conversation.Conversation, error) {
	return get(ctx, s.DB, a, id, false)
}

// List uses a stable ascending UUID order, unaffected by title edits. Empty pages
// are JSON arrays and pagination never returns a row solely because its ID is known.
func (s *Store) List(ctx context.Context, a identity.Actor, after string, limit int) ([]conversation.Conversation, error) {
	if limit < 1 || limit > 101 || (after != "" && !conversation.ValidID(after)) {
		return nil, policy.ErrInvalid
	}
	if after == "" {
		after = uuid.Nil.String()
	}
	rows, err := s.DB.Query(ctx, projection+" AND c.id>$3 ORDER BY c.id LIMIT $4", a.ProjectID, a.User.ID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []conversation.Conversation{}
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}

// lockProject serializes writes with Project policy changes and deactivation.
func lockProject(ctx context.Context, tx pgx.Tx, project string) error {
	var active bool
	err := tx.QueryRow(ctx, "SELECT status='active' FROM chat.projects WHERE id=$1 FOR SHARE", project).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.ErrUnauthenticated
	}
	if err != nil {
		return err
	}
	if !active {
		return auth.ErrUnauthenticated
	}
	return nil
}

// lockUsers checks existence in this tenant and current actor restrictions under
// sorted user locks. Invited banned users remain read-only; system actors cannot join.
func lockUsers(ctx context.Context, tx pgx.Tx, a identity.Actor, ids []string, inviting bool) error {
	rows, err := tx.Query(ctx, "SELECT id::text,kind,banned_at IS NOT NULL FROM chat.users WHERE project_id=$1 AND id=ANY($2::uuid[]) ORDER BY id FOR SHARE", a.ProjectID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	bots := false
	for rows.Next() {
		var id, kind string
		var banned bool
		if err := rows.Scan(&id, &kind, &banned); err != nil {
			return err
		}
		count++
		if id == a.User.ID {
			if banned {
				return identity.ErrBanned
			}
			if kind != "human" {
				return policy.ErrForbidden
			}
		}
		if kind == "system" {
			return policy.ErrForbidden
		}
		bots = bots || kind == "bot"
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != len(ids) {
		return identity.ErrNotFound
	}
	if bots && inviting {
		var allowed bool
		if err := tx.QueryRow(ctx, "SELECT COALESCE((flags->>'allow_bots')::boolean,false) FROM chat.project_settings WHERE project_id=$1", a.ProjectID).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			return policy.ErrFeatureDisabled
		}
	}
	return nil
}

// audit records metadata only, in the mutation's transaction. An invalid trace or
// unavailable audit store rolls the conversation and its initial memberships back.
func audit(ctx context.Context, tx pgx.Tx, a identity.Actor, id, action string, fields map[string]any, tr policy.Trace) error {
	key, err := uuid.NewV7()
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO chat.audit_logs(id,project_id,actor_id,actor_type,action,resource_type,resource_id,metadata,ip,request_id)
 VALUES($1,$2,$3,'user',$4,'conversation',$5,$6,NULLIF($7,'')::inet,$8)`, key.String(), a.ProjectID, a.User.ID, action, id, metadata, tr.IP, tr.RequestID)
	if err == nil {
		// Replay must observe the same committed changes as the audit. Payload is
		// references/changed-field names only, without private titles or content.
		_, err = eventrepo.Append(ctx, tx, a.ProjectID, id, action, fields)
	}
	return err
}

// Create locks only a specific canonical DIRECT key, not the entire Project.
// Hash collisions can cause extra waiting but cannot merge pairs: the SQL PK uses
// the complete Project/low/high tuple. The second SELECT sees the winner's commit.
func (s *Store) Create(ctx context.Context, a identity.Actor, p conversation.Create, tr policy.Trace) (conversation.Conversation, bool, error) {
	if err := p.Validate(a.User.ID); err != nil {
		return conversation.Conversation{}, false, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return conversation.Conversation{}, false, err
	}
	defer tx.Rollback(ctx)
	if err = lockProject(ctx, tx, a.ProjectID); err != nil {
		return conversation.Conversation{}, false, err
	}
	ids := append([]string{a.User.ID}, p.MemberIDs...)
	sort.Strings(ids)
	if err = lockUsers(ctx, tx, a, ids, true); err != nil {
		return conversation.Conversation{}, false, err
	}
	if p.Type == "DIRECT" {
		key := "chat.direct:" + a.ProjectID + ":" + ids[0] + ":" + ids[1]
		if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", key); err != nil {
			return conversation.Conversation{}, false, err
		}
		var existing string
		err = tx.QueryRow(ctx, "SELECT conversation_id::text FROM chat.direct_pairs WHERE project_id=$1 AND low_user_id=$2 AND high_user_id=$3", a.ProjectID, ids[0], ids[1]).Scan(&existing)
		if err == nil {
			c, err := get(ctx, tx, a, existing, false)
			if err != nil {
				return c, false, err
			}
			return c, false, tx.Commit(ctx)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return conversation.Conversation{}, false, err
		}
	}
	id, err := uuid.NewV7()
	if err != nil {
		return conversation.Conversation{}, false, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO chat.conversations(id,project_id,type,title,avatar_url,created_by) VALUES($1,$2,$3,$4,$5,$6)", id.String(), a.ProjectID, p.Type, p.Title, p.AvatarURL, a.User.ID); err != nil {
		return conversation.Conversation{}, false, err
	}
	if p.Type == "DIRECT" {
		if _, err = tx.Exec(ctx, "INSERT INTO chat.direct_pairs(project_id,low_user_id,high_user_id,conversation_id) VALUES($1,$2,$3,$4)", a.ProjectID, ids[0], ids[1], id.String()); err != nil {
			return conversation.Conversation{}, false, err
		}
	}
	for _, user := range ids {
		role := "member"
		if user == a.User.ID && p.Type != "DIRECT" {
			role = "moderator"
		}
		if _, err = tx.Exec(ctx, "INSERT INTO chat.conversation_members(project_id,conversation_id,user_id,role) VALUES($1,$2,$3,$4)", a.ProjectID, id.String(), user, role); err != nil {
			return conversation.Conversation{}, false, err
		}
	}
	if err = audit(ctx, tx, a, id.String(), "conversation.created", map[string]any{"type": p.Type, "member_count": len(ids)}, tr); err != nil {
		return conversation.Conversation{}, false, err
	}
	c, err := get(ctx, tx, a, id.String(), false)
	if err != nil {
		return c, false, err
	}
	// Commit also runs the deferred complete-membership DB constraint.
	if err = tx.Commit(ctx); err != nil {
		return conversation.Conversation{}, false, err
	}
	return c, true, nil
}

// Patch serializes Project → actor → conversation → membership. Role, ban and
// version are checked in the same transaction as the update and audit append.
func (s *Store) Patch(ctx context.Context, a identity.Actor, id string, p conversation.Patch, tr policy.Trace) (conversation.Conversation, error) {
	if err := p.Validate(); err != nil {
		return conversation.Conversation{}, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return conversation.Conversation{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockProject(ctx, tx, a.ProjectID); err != nil {
		return conversation.Conversation{}, err
	}
	if err = lockUsers(ctx, tx, a, []string{a.User.ID}, false); err != nil {
		return conversation.Conversation{}, err
	}
	var locked string
	if err = tx.QueryRow(ctx, "SELECT id::text FROM chat.conversations WHERE project_id=$1 AND id=$2 AND deleted_at IS NULL FOR UPDATE", a.ProjectID, id).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = identity.ErrNotFound
		}
		return conversation.Conversation{}, err
	}
	c, err := get(ctx, tx, a, id, true)
	if err != nil {
		return c, err
	}
	if c.Membership.Banned {
		return c, identity.ErrBanned
	}
	if c.Type == "DIRECT" {
		return c, conversation.ErrUnsupported
	}
	if c.Membership.Role != "moderator" {
		return c, policy.ErrForbidden
	}
	if c.Version != p.ExpectedVersion {
		return c, policy.ErrConflict
	}
	if _, err = tx.Exec(ctx, "UPDATE chat.conversations SET title=COALESCE($3,title),avatar_url=COALESCE($4,avatar_url),resource_version=resource_version+1,updated_at=clock_timestamp() WHERE project_id=$1 AND id=$2", a.ProjectID, id, p.Title, p.AvatarURL); err != nil {
		return c, err
	}
	fields := []string{}
	if p.Title != nil {
		fields = append(fields, "title")
	}
	if p.AvatarURL != nil {
		fields = append(fields, "avatar_url")
	}
	if err = audit(ctx, tx, a, id, "conversation.updated", map[string]any{"changed_fields": fields}, tr); err != nil {
		return c, err
	}
	c, err = get(ctx, tx, a, id, false)
	if err != nil {
		return c, err
	}
	return c, tx.Commit(ctx)
}
