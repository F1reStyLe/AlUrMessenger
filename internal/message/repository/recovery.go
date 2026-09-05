package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/F1reStyLe/AlUrMessenger/internal/event"
	eventrepo "github.com/F1reStyLe/AlUrMessenger/internal/event/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/message"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/jackc/pgx/v5"
)

// Replay reads a consistent event high watermark under current membership. Hidden
// read-receipts advance the cursor without leaking the other user's checkpoint.
func (s *Store) Replay(ctx context.Context, a identity.Actor, id string, after int64, limit int) (message.Replay, error) {
	result := message.Replay{Events: []event.Envelope{}, Through: after}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	if err = readAccess(ctx, tx, a, id); err != nil {
		return result, err
	}
	var receipts bool
	if err = tx.QueryRow(ctx, "SELECT c.event_sequence,COALESCE((s.flags->>'read_receipts')::boolean,false) FROM chat.conversations c JOIN chat.project_settings s ON s.project_id=c.project_id WHERE c.project_id=$1 AND c.id=$2", a.ProjectID, id).Scan(&result.High, &receipts); err != nil {
		return result, err
	}
	if after > result.High {
		return result, message.ErrResync
	}
	rows, err := tx.Query(ctx, "SELECT envelope FROM chat.conversation_events WHERE project_id=$1 AND conversation_id=$2 AND sequence>$3 AND sequence<=$4 ORDER BY sequence LIMIT $5", a.ProjectID, id, after, result.High, limit)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var raw []byte
		var e event.Envelope
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return result, err
		}
		if err = json.Unmarshal(raw, &e); err != nil {
			rows.Close()
			return result, err
		}
		if e.Sequence != result.Through+1 {
			rows.Close()
			return result, message.ErrResync
		}
		if !receipts && (e.Type == "message.read" || e.Type == "message.delivered") && e.Payload["user_id"] != a.User.ID {
			e.Type = "sync.advance"
			e.Payload = map[string]any{}
		}
		result.Events = append(result.Events, e)
		result.Through = e.Sequence
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if result.Through < result.High && len(result.Events) < limit {
		return result, message.ErrResync
	}
	// Durable storage stays reference-only. Hydrate each distinct message once
	// from this authorized snapshot, including for its older created/updated events.
	// This is current state, not a historical revision: deletion must never replay
	// the former body. Keep the historical envelope/version for cursor identity.
	ids := []string{}
	for _, e := range result.Events {
		if messageStateEvent(e.Type) {
			messageID, ok := e.Payload["message_id"].(string)
			if !ok || messageID == "" {
				return result, message.ErrResync
			}
			ids = append(ids, messageID)
		}
	}
	if len(ids) > 0 {
		current := map[string]message.Message{}
		rows, err = tx.Query(ctx, "SELECT "+columns+" FROM chat.messages m WHERE m.project_id=$1 AND m.conversation_id=$2 AND m.id=ANY($3::uuid[])", a.ProjectID, id, ids)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			m, e := s.scan(a.ProjectID, rows)
			if e != nil {
				rows.Close()
				return result, e
			}
			current[m.ID] = m
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return result, err
		}
		for _, e := range result.Events {
			if !messageStateEvent(e.Type) {
				continue
			}
			m, ok := current[e.Payload["message_id"].(string)]
			if !ok {
				return result, message.ErrResync
			}
			e.Payload["message"] = m
		}
	}
	result.More = result.Through < result.High
	return result, tx.Commit(ctx)
}

// Snapshot holds one REPEATABLE READ transaction through counters, metadata and
// decryption; a concurrent send is either wholly before or wholly after this snapshot.
func (s *Store) Snapshot(ctx context.Context, a identity.Actor, id string) (message.Snapshot, error) {
	result := message.Snapshot{Messages: []message.Message{}, Pins: []string{}}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	if err = readAccess(ctx, tx, a, id); err != nil {
		return result, err
	}
	c := &result.Conversation
	m := &c.Membership
	k := &result.Checkpoint
	err = tx.QueryRow(ctx, `SELECT c.id::text,c.type,c.title,c.avatar_url,c.created_by::text,c.created_at,c.updated_at,c.resource_version,
 cm.user_id::text,cm.role,cm.joined_at,cm.banned,cm.version,cm.last_read_sequence,cm.last_delivered_sequence,c.event_sequence,c.message_sequence,c.last_message_id::text
 FROM chat.conversations c JOIN chat.conversation_members cm ON cm.project_id=c.project_id AND cm.conversation_id=c.id WHERE c.project_id=$1 AND c.id=$2 AND cm.user_id=$3`, a.ProjectID, id, a.User.ID).Scan(&c.ID, &c.Type, &c.Title, &c.AvatarURL, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt, &c.Version, &m.UserID, &m.Role, &m.JoinedAt, &m.Banned, &m.Version, &k.Read, &k.Delivered, &result.EventSequence, &result.MessageSequence, &c.LastMessageID)
	if err != nil {
		return result, err
	}
	k.UserID = a.User.ID
	k.Version = m.Version
	m.Read, m.Delivered = k.Read, k.Delivered
	c.MessageSequence, c.EventSequence = result.MessageSequence, result.EventSequence
	rows, err := tx.Query(ctx, "SELECT "+columns+" FROM chat.messages m WHERE m.project_id=$1 AND m.conversation_id=$2 ORDER BY m.sequence DESC LIMIT 50", a.ProjectID, id)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		m, e := s.scan(a.ProjectID, rows)
		if e != nil {
			rows.Close()
			return result, e
		}
		result.Messages = append(result.Messages, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	slices.Reverse(result.Messages)
	pins, err := readPins(ctx, tx, a.ProjectID, id)
	if err != nil {
		return result, err
	}
	for _, pin := range pins {
		result.Pins = append(result.Pins, pin.MessageID)
	}
	return result, tx.Commit(ctx)
}

// Every message-affecting event hydrates the whole current resource, including
// reactions and pin. Historical references never act as client-side state deltas.
func messageStateEvent(kind string) bool {
	switch kind {
	case "message.created", "message.updated", "message.deleted", "reaction.created", "reaction.deleted", "message.pinned", "message.unpinned":
		return true
	default:
		return false
	}
}

// Receipt uses Project→user→conversation→membership lock order. All members can
// acknowledge a CHANNEL, but Project/conversation bans prohibit domain writes.
func (s *Store) Receipt(ctx context.Context, a identity.Actor, id, kind string, sequence int64) (message.Checkpoint, error) {
	k := message.Checkpoint{UserID: a.User.ID}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return k, err
	}
	defer tx.Rollback(ctx)
	var enabled bool
	err = tx.QueryRow(ctx, `SELECT COALESCE((s.flags->>'read_receipts')::boolean,false) FROM chat.projects p JOIN chat.project_settings s ON s.project_id=p.id WHERE p.id=$1 AND p.status='active' FOR SHARE OF p`, a.ProjectID).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return k, identity.ErrNotFound
	}
	if err != nil {
		return k, err
	}
	var banned bool
	if err = tx.QueryRow(ctx, "SELECT banned_at IS NOT NULL FROM chat.users WHERE project_id=$1 AND id=$2 FOR SHARE", a.ProjectID, a.User.ID).Scan(&banned); err != nil {
		return k, err
	}
	if banned {
		return k, identity.ErrBanned
	}
	var high int64
	err = tx.QueryRow(ctx, "SELECT message_sequence FROM chat.conversations WHERE project_id=$1 AND id=$2 AND deleted_at IS NULL FOR UPDATE", a.ProjectID, id).Scan(&high)
	if errors.Is(err, pgx.ErrNoRows) {
		return k, identity.ErrNotFound
	}
	if err != nil {
		return k, err
	}
	err = tx.QueryRow(ctx, "SELECT last_read_sequence,last_delivered_sequence,version,banned FROM chat.conversation_members WHERE project_id=$1 AND conversation_id=$2 AND user_id=$3 AND left_at IS NULL FOR UPDATE", a.ProjectID, id, a.User.ID).Scan(&k.Read, &k.Delivered, &k.Version, &banned)
	if errors.Is(err, pgx.ErrNoRows) {
		return k, identity.ErrNotFound
	}
	if err != nil {
		return k, err
	}
	if banned {
		return k, identity.ErrBanned
	}
	if sequence > high {
		return k, policy.ErrInvalid
	}
	// Disabled receipts suppress publication to peers, but the owner's private read
	// position still synchronizes between devices. Replay replaces peer events with advance.
	_ = enabled
	nextRead := k.Read
	if kind == "read" {
		nextRead = max(nextRead, sequence)
	}
	nextDelivered := max(k.Delivered, sequence)
	if nextRead == k.Read && nextDelivered == k.Delivered {
		return k, tx.Commit(ctx)
	}
	err = tx.QueryRow(ctx, "UPDATE chat.conversation_members SET last_read_sequence=$4,last_delivered_sequence=$5,version=version+1 WHERE project_id=$1 AND conversation_id=$2 AND user_id=$3 RETURNING last_read_sequence,last_delivered_sequence,version", a.ProjectID, id, a.User.ID, nextRead, nextDelivered).Scan(&k.Read, &k.Delivered, &k.Version)
	if err != nil {
		return k, err
	}
	_, err = eventrepo.Append(ctx, tx, a.ProjectID, id, "message."+kind, map[string]any{"user_id": a.User.ID, "last_read_sequence": fmt.Sprint(k.Read), "last_delivered_sequence": fmt.Sprint(k.Delivered), "member_version": fmt.Sprint(k.Version)})
	if err != nil {
		return k, err
	}
	return k, tx.Commit(ctx)
}
