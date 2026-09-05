package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/F1reStyLe/AlUrMessenger/internal/cryptography"
	eventrepo "github.com/F1reStyLe/AlUrMessenger/internal/event/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/message"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/jackc/pgx/v5"
)

// requireFeature runs while writeAccess holds the Project lock. Settings updates
// lock that same row, so a flag cannot change between authorization and commit.
func requireFeature(ctx context.Context, tx pgx.Tx, project, flag string) error {
	var enabled bool
	err := tx.QueryRow(ctx, "SELECT COALESCE((flags->>$2)::boolean,false) FROM chat.project_settings WHERE project_id=$1", project, flag).Scan(&enabled)
	if err != nil {
		return err
	}
	if !enabled {
		return policy.ErrFeatureDisabled
	}
	return nil
}

// mutationAccess discovers the conversation without taking a message lock first.
// All writes then serialize Project → actor → conversation → membership → message;
// reply creation, last_message_id updates and concurrent edits share that ordering.
// The optional WS scope is checked before locking or changing any resource.
func mutationAccess(ctx context.Context, tx pgx.Tx, a identity.Actor, id, scope, flag string) (string, error) {
	var conversation string
	err := tx.QueryRow(ctx, "SELECT conversation_id::text FROM chat.messages WHERE project_id=$1 AND id=$2", a.ProjectID, id).Scan(&conversation)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", identity.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if scope != "" && scope != conversation {
		return "", identity.ErrNotFound
	}
	if _, err = writeAccess(ctx, tx, a, conversation, "", false); err != nil {
		return "", err
	}
	var sender, kind string
	if err = tx.QueryRow(ctx, "SELECT sender_id::text,type FROM chat.messages WHERE project_id=$1 AND id=$2 FOR UPDATE", a.ProjectID, id).Scan(&sender, &kind); err != nil {
		return "", err
	}
	// Neither Project admin nor conversation moderator may edit another author's
	// message via this user endpoint. Moderation belongs to its separate future API.
	if sender != a.User.ID || kind == "SYSTEM" {
		return "", policy.ErrForbidden
	}
	if err = requireFeature(ctx, tx, a.ProjectID, flag); err != nil {
		return "", err
	}
	return conversation, nil
}

// Edit replaces ciphertext and the complete blind index atomically, keeping the
// original sequence, reply reference, metadata, TTL and send fingerprint intact.
func (s *Store) Edit(ctx context.Context, a identity.Actor, id, scope string, p message.Edit) (message.Message, error) {
	if err := p.Validate(); err != nil {
		return message.Message{}, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return message.Message{}, err
	}
	defer tx.Rollback(ctx)
	conversation, err := mutationAccess(ctx, tx, a, id, scope, "allow_edit")
	if err != nil {
		return message.Message{}, err
	}
	m, err := s.scan(a.ProjectID, tx.QueryRow(ctx, "SELECT "+columns+" FROM chat.messages m WHERE m.project_id=$1 AND m.id=$2", a.ProjectID, id))
	if err != nil {
		return m, err
	}
	if m.Status != "active" {
		return message.Message{}, identity.ErrNotFound
	}
	if m.Type != "TEXT" {
		return message.Message{}, policy.ErrForbidden
	}
	if m.Version != p.ExpectedVersion {
		return message.Message{}, policy.ErrConflict
	}
	searchVersion, tokens, err := s.Crypto.Index(a.ProjectID, p.Content.Text)
	if errors.Is(err, cryptography.ErrTokens) {
		return message.Message{}, policy.ErrInvalid
	}
	if err != nil {
		return message.Message{}, err
	}
	plain, err := json.Marshal(message.Payload{Content: p.Content, Metadata: m.Metadata, Forward: m.Forward})
	if err != nil {
		return message.Message{}, err
	}
	envelope, err := s.Crypto.Seal(a.ProjectID, conversation, id, plain)
	if err != nil {
		return message.Message{}, err
	}
	// Even an equal-text edit advances version when its precondition matches.
	// No historical body is retained in events, audit, receipts or a revisions table.
	if _, err = tx.Exec(ctx, `UPDATE chat.messages SET encrypted_content=$3,nonce=$4,key_version=$5,payload_version=$6,
 resource_version=resource_version+1,edited_at=clock_timestamp() WHERE project_id=$1 AND id=$2`, a.ProjectID, id, envelope.Ciphertext, envelope.Nonce, envelope.KeyVersion, envelope.PayloadVersion); err != nil {
		return message.Message{}, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM chat.message_search WHERE project_id=$1 AND message_id=$2", a.ProjectID, id); err != nil {
		return message.Message{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO chat.message_search(project_id,conversation_id,message_id,search_key_version,tokens) VALUES($1,$2,$3,$4,$5)", a.ProjectID, conversation, id, searchVersion, tokens); err != nil {
		return message.Message{}, err
	}
	m, err = s.scan(a.ProjectID, tx.QueryRow(ctx, "SELECT "+columns+" FROM chat.messages m WHERE m.project_id=$1 AND m.id=$2", a.ProjectID, id))
	if err != nil {
		return message.Message{}, err
	}
	if err = appendMutation(ctx, tx, a.ProjectID, conversation, "message.updated", m); err != nil {
		return message.Message{}, err
	}
	return m, tx.Commit(ctx)
}

// Delete wipes the encrypted body and its blind index while retaining the public
// identity and sequence. Repeated deletion returns the same tombstone/version;
// current membership, author permission, bans and flags are still checked first.
func (s *Store) Delete(ctx context.Context, a identity.Actor, id, scope string) (message.Message, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return message.Message{}, err
	}
	defer tx.Rollback(ctx)
	conversation, err := mutationAccess(ctx, tx, a, id, scope, "allow_delete")
	if err != nil {
		return message.Message{}, err
	}
	// Deletion must remain possible when the payload key is unavailable or corrupt.
	var terminal bool
	if err = tx.QueryRow(ctx, "SELECT deleted_at IS NOT NULL FROM chat.messages WHERE project_id=$1 AND id=$2", a.ProjectID, id).Scan(&terminal); err != nil {
		return message.Message{}, err
	}
	if !terminal {
		// Deletion invalidates relation state in the same transaction and event.
		// Old reaction/pin events hydrate this tombstone rather than applying deltas.
		for _, table := range []string{"message_reactions", "pinned_messages"} {
			if _, err = tx.Exec(ctx, "DELETE FROM chat."+table+" WHERE project_id=$1 AND conversation_id=$2 AND message_id=$3", a.ProjectID, conversation, id); err != nil {
				return message.Message{}, err
			}
		}
		// Logical attachment access ends with the source message. Shared object
		// bytes remain ready while forwards reference their own logical rows; the
		// retention reconciler later deletes an object only at reference count zero.
		if _, err = tx.Exec(ctx, `UPDATE chat.attachments SET status='deleted' WHERE project_id=$1 AND id IN
 (SELECT attachment_id FROM chat.message_attachments WHERE project_id=$1 AND conversation_id=$2 AND message_id=$3)`, a.ProjectID, conversation, id); err != nil {
			return message.Message{}, err
		}
		if _, err = tx.Exec(ctx, "DELETE FROM chat.message_attachments WHERE project_id=$1 AND conversation_id=$2 AND message_id=$3", a.ProjectID, conversation, id); err != nil {
			return message.Message{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE chat.messages SET encrypted_content=''::bytea,reply_to_message_id=NULL,
 deleted_at=clock_timestamp(),resource_version=resource_version+1 WHERE project_id=$1 AND id=$2`, a.ProjectID, id); err != nil {
			return message.Message{}, err
		}
		if _, err = tx.Exec(ctx, "DELETE FROM chat.message_search WHERE project_id=$1 AND message_id=$2", a.ProjectID, id); err != nil {
			return message.Message{}, err
		}
		// The previous message may also have expired; choose the latest live row.
		if _, err = tx.Exec(ctx, `UPDATE chat.conversations SET last_message_id=(SELECT id FROM chat.messages
 WHERE project_id=$1 AND conversation_id=$2 AND deleted_at IS NULL AND expires_at>clock_timestamp() ORDER BY sequence DESC LIMIT 1),
 updated_at=clock_timestamp() WHERE project_id=$1 AND id=$2`, a.ProjectID, conversation); err != nil {
			return message.Message{}, err
		}
	}
	m, err := s.scan(a.ProjectID, tx.QueryRow(ctx, "SELECT "+columns+" FROM chat.messages m WHERE m.project_id=$1 AND m.id=$2", a.ProjectID, id))
	if err != nil {
		return message.Message{}, err
	}
	if !terminal {
		if err = appendMutation(ctx, tx, a.ProjectID, conversation, "message.deleted", m); err != nil {
			return message.Message{}, err
		}
	}
	return m, tx.Commit(ctx)
}

// appendMutation adds a reference only; replay resolves current state under read
// authorization. Editing/deleting never allocates another message sequence.
func appendMutation(ctx context.Context, tx pgx.Tx, project, conversation, kind string, m message.Message) error {
	_, err := eventrepo.Append(ctx, tx, project, conversation, kind, map[string]any{
		"message_id": m.ID, "message_sequence": fmt.Sprint(m.Sequence), "resource_version": fmt.Sprint(m.Version),
	})
	return err
}
