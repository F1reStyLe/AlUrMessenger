package repository

import (
	"context"
	"errors"

	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/message"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/jackc/pgx/v5"
)

// relationAccess serializes associations with sends, edits, deletes and role
// changes. Discovery is tenant-scoped and WS scope is checked before any mutation.
// Unlike content editing, reactions require membership, not authorship or CHANNEL
// moderator status. Pinning always requires a GROUP/CHANNEL moderator; Project
// admin does not bypass this ordinary API permission and DIRECT has no moderators.
func relationAccess(ctx context.Context, tx pgx.Tx, a identity.Actor, id, scope, flag string, pin bool) (string, bool, error) {
	var conv string
	err := tx.QueryRow(ctx, "SELECT conversation_id::text FROM chat.messages WHERE project_id=$1 AND id=$2", a.ProjectID, id).Scan(&conv)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, identity.ErrNotFound
	}
	if err != nil {
		return "", false, err
	}
	if scope != "" && scope != conv {
		return "", false, identity.ErrNotFound
	}
	if _, err = activityAccess(ctx, tx, a, conv, "", false, false); err != nil {
		return "", false, err
	}
	if pin {
		var moderator bool
		err = tx.QueryRow(ctx, `SELECT c.type IN ('GROUP','CHANNEL') AND cm.role='moderator' FROM chat.conversations c
 JOIN chat.conversation_members cm ON cm.project_id=c.project_id AND cm.conversation_id=c.id
 WHERE c.project_id=$1 AND c.id=$2 AND cm.user_id=$3`, a.ProjectID, conv, a.User.ID).Scan(&moderator)
		if err != nil {
			return "", false, err
		}
		if !moderator {
			return "", false, policy.ErrForbidden
		}
	}
	if err = requireFeature(ctx, tx, a.ProjectID, flag); err != nil {
		return "", false, err
	}
	var live bool
	err = tx.QueryRow(ctx, "SELECT deleted_at IS NULL AND expires_at>clock_timestamp() FROM chat.messages WHERE project_id=$1 AND id=$2 FOR UPDATE", a.ProjectID, id).Scan(&live)
	return conv, live, err
}

// React canonicalizes even for direct repository callers. The composite PK makes
// duplicate adds safe; conversation locking makes version/event changes match the
// actual relation change exactly. Removing an absent relation is a true no-op.
func (s *Store) React(ctx context.Context, a identity.Actor, id, scope, emoji string, add bool) (message.Message, error) {
	emoji, err := message.NormalizeReaction(emoji)
	if err != nil {
		return message.Message{}, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return message.Message{}, err
	}
	defer tx.Rollback(ctx)
	conv, live, err := relationAccess(ctx, tx, a, id, scope, "allow_reactions", false)
	if err != nil {
		return message.Message{}, err
	}
	if add && !live {
		return message.Message{}, identity.ErrNotFound
	}
	var changed bool
	if live {
		if add {
			// Bound DTO size by emoji kinds, without limiting the number of users
			// who may choose an existing reaction. Retries remain valid at the cap.
			var permitted bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chat.message_reactions WHERE project_id=$1 AND message_id=$2 AND reaction=$3)
 OR (SELECT count(DISTINCT reaction) FROM chat.message_reactions WHERE project_id=$1 AND message_id=$2)<32`, a.ProjectID, id, emoji).Scan(&permitted)
			if err != nil {
				return message.Message{}, err
			}
			if !permitted {
				return message.Message{}, policy.ErrInvalid
			}
			tag, e := tx.Exec(ctx, "INSERT INTO chat.message_reactions(project_id,conversation_id,message_id,user_id,reaction) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING", a.ProjectID, conv, id, a.User.ID, emoji)
			err, changed = e, tag.RowsAffected() > 0
		} else {
			tag, e := tx.Exec(ctx, "DELETE FROM chat.message_reactions WHERE project_id=$1 AND message_id=$2 AND user_id=$3 AND reaction=$4", a.ProjectID, id, a.User.ID, emoji)
			err, changed = e, tag.RowsAffected() > 0
		}
		if err != nil {
			return message.Message{}, err
		}
	}
	kind := "reaction.deleted"
	if add {
		kind = "reaction.created"
	}
	m, err := s.relationResult(ctx, tx, a, conv, id, kind, changed)
	if err != nil {
		return m, err
	}
	return m, tx.Commit(ctx)
}

// SetPin never reassigns an existing pin's creator/time. Pin collections are
// bounded to 100 live messages, and all changes share the message resource_version.
func (s *Store) SetPin(ctx context.Context, a identity.Actor, id, scope string, add bool) (message.Message, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return message.Message{}, err
	}
	defer tx.Rollback(ctx)
	conv, live, err := relationAccess(ctx, tx, a, id, scope, "allow_pin", true)
	if err != nil {
		return message.Message{}, err
	}
	if add && !live {
		return message.Message{}, identity.ErrNotFound
	}
	var changed bool
	if live {
		if add {
			var permitted bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chat.pinned_messages WHERE project_id=$1 AND conversation_id=$2 AND message_id=$3)
 OR (SELECT count(*) FROM chat.pinned_messages p JOIN chat.messages m ON m.project_id=p.project_id AND m.conversation_id=p.conversation_id AND m.id=p.message_id
 WHERE p.project_id=$1 AND p.conversation_id=$2 AND m.deleted_at IS NULL AND m.expires_at>clock_timestamp())<100`, a.ProjectID, conv, id).Scan(&permitted)
			if err != nil {
				return message.Message{}, err
			}
			if !permitted {
				return message.Message{}, policy.ErrInvalid
			}
			tag, e := tx.Exec(ctx, "INSERT INTO chat.pinned_messages(project_id,conversation_id,message_id,pinned_by) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", a.ProjectID, conv, id, a.User.ID)
			err, changed = e, tag.RowsAffected() > 0
		} else {
			tag, e := tx.Exec(ctx, "DELETE FROM chat.pinned_messages WHERE project_id=$1 AND conversation_id=$2 AND message_id=$3", a.ProjectID, conv, id)
			err, changed = e, tag.RowsAffected() > 0
		}
		if err != nil {
			return message.Message{}, err
		}
	}
	kind := "message.unpinned"
	if add {
		kind = "message.pinned"
	}
	m, err := s.relationResult(ctx, tx, a, conv, id, kind, changed)
	if err != nil {
		return m, err
	}
	return m, tx.Commit(ctx)
}

// relationResult publishes references only, never reaction deltas that can
// resurrect an earlier state during replay. Hydration supplies current aggregates.
// A failed projection/event append rolls back the association and its version.
func (s *Store) relationResult(ctx context.Context, tx pgx.Tx, a identity.Actor, conv, id, kind string, changed bool) (message.Message, error) {
	if changed {
		if _, err := tx.Exec(ctx, "UPDATE chat.messages SET resource_version=resource_version+1 WHERE project_id=$1 AND id=$2", a.ProjectID, id); err != nil {
			return message.Message{}, err
		}
	}
	m, err := s.scan(a.ProjectID, tx.QueryRow(ctx, "SELECT "+columns+" FROM chat.messages m WHERE m.project_id=$1 AND m.id=$2", a.ProjectID, id))
	if err != nil {
		return m, err
	}
	if changed {
		err = appendMutation(ctx, tx, a.ProjectID, conv, kind, m)
	}
	return m, err
}

// readPins is also used by Snapshot, preserving its exact PostgreSQL snapshot.
// Expired rows are hidden immediately; physical cleanup is retention's concern.
func readPins(ctx context.Context, tx pgx.Tx, project, conv string) ([]message.Pin, error) {
	items := []message.Pin{}
	rows, err := tx.Query(ctx, `SELECT p.message_id::text,p.pinned_by::text,p.created_at,m.resource_version
 FROM chat.pinned_messages p JOIN chat.messages m ON m.project_id=p.project_id AND m.conversation_id=p.conversation_id AND m.id=p.message_id
 WHERE p.project_id=$1 AND p.conversation_id=$2 AND m.deleted_at IS NULL AND m.expires_at>clock_timestamp() ORDER BY m.sequence`, project, conv)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p message.Pin
		if err = rows.Scan(&p.MessageID, &p.PinnedBy, &p.CreatedAt, &p.Version); err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}

func (s *Store) Pins(ctx context.Context, a identity.Actor, id string) (message.PinList, error) {
	result := message.PinList{Items: []message.Pin{}}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	if err = readAccess(ctx, tx, a, id); err != nil {
		return result, err
	}
	if err = tx.QueryRow(ctx, "SELECT event_sequence FROM chat.conversations WHERE project_id=$1 AND id=$2", a.ProjectID, id).Scan(&result.EventSequence); err != nil {
		return result, err
	}
	result.Items, err = readPins(ctx, tx, a.ProjectID, id)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
