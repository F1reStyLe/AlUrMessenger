package repository

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/internal/cryptography"
	eventrepo "github.com/F1reStyLe/AlUrMessenger/internal/event/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/message"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// forward creates a fully independent encrypted TEXT message. A per-Project
// advisory lock serializes the only operation that spans two conversations,
// preventing inverse source/target forwards from taking row locks in opposite
// order. Ordinary single-conversation operations never wait for this lock.
func (s *Store) forward(ctx context.Context, a identity.Actor, target string, command message.Send) (message.Sent, error) {
	canonical, err := command.Canonical(target)
	if err != nil {
		return message.Sent{}, policy.ErrInvalid
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return message.Sent{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "message.forward:"+a.ProjectID); err != nil {
		return message.Sent{}, err
	}
	retention, err := writeAccess(ctx, tx, a, target, command.ClientID, false)
	if err != nil {
		return message.Sent{}, err
	}
	if err = requireFeature(ctx, tx, a.ProjectID, "allow_forward"); err != nil {
		return message.Sent{}, err
	}

	// Matching retries return the committed target's current state. They recheck
	// target access/flag but do not depend on a source that may since be deleted,
	// expired, left or physically purged.
	var existing, fingerprintVersion string
	var fingerprint []byte
	var dedupExpires time.Time
	err = tx.QueryRow(ctx, "SELECT message_id::text,fingerprint_version,fingerprint,expires_at FROM chat.message_idempotency WHERE project_id=$1 AND sender_id=$2 AND client_message_id=$3", a.ProjectID, a.User.ID, command.ClientID).Scan(&existing, &fingerprintVersion, &fingerprint, &dedupExpires)
	if err == nil && dedupExpires.After(time.Now()) {
		candidate, e := s.Crypto.Fingerprint(a.ProjectID, fingerprintVersion, canonical)
		if e != nil {
			return message.Sent{}, e
		}
		if !hmac.Equal(candidate, fingerprint) {
			return message.Sent{}, message.ErrIdempotencyConflict
		}
		m, e := s.scan(a.ProjectID, tx.QueryRow(ctx, "SELECT "+columns+" FROM chat.messages m WHERE m.project_id=$1 AND m.id=$2", a.ProjectID, existing))
		if e != nil {
			return message.Sent{}, e
		}
		if e = tx.Commit(ctx); e != nil {
			return message.Sent{}, e
		}
		return message.Sent{Message: m, Status: "SENT", Deduplicated: true, DedupExpiresAt: dedupExpires}, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return message.Sent{}, err
	}
	if existing != "" {
		if _, err = tx.Exec(ctx, "DELETE FROM chat.message_idempotency WHERE project_id=$1 AND sender_id=$2 AND client_message_id=$3", a.ProjectID, a.User.ID, command.ClientID); err != nil {
			return message.Sent{}, err
		}
	}

	// Discover within the actor's Project, then require current source membership
	// and lock the live row through snapshot extraction. Cross-Project UUIDs and
	// inaccessible private conversations are deliberately indistinguishable.
	sourceID := *command.ForwardFrom
	var sourceConversation string
	err = tx.QueryRow(ctx, "SELECT conversation_id::text FROM chat.messages WHERE project_id=$1 AND id=$2 AND deleted_at IS NULL AND expires_at>clock_timestamp()", a.ProjectID, sourceID).Scan(&sourceConversation)
	if errors.Is(err, pgx.ErrNoRows) {
		return message.Sent{}, identity.ErrNotFound
	}
	if err != nil {
		return message.Sent{}, err
	}
	if err = readAccess(ctx, tx, a, sourceConversation); err != nil {
		return message.Sent{}, err
	}
	source, err := s.scan(a.ProjectID, tx.QueryRow(ctx, "SELECT "+columns+" FROM chat.messages m WHERE m.project_id=$1 AND m.id=$2 AND m.deleted_at IS NULL AND m.expires_at>clock_timestamp() FOR SHARE OF m", a.ProjectID, sourceID))
	if err != nil {
		return message.Sent{}, err
	}
	if source.Status != "active" || source.Content == nil {
		return message.Sent{}, identity.ErrNotFound
	}
	// IMAGE copying requires attachment ownership/storage rules from Phase 6;
	// SYSTEM messages are service output and cannot be impersonated by clients.
	if source.Type != "TEXT" {
		return message.Sent{}, policy.ErrForbidden
	}
	original := message.SenderSnapshot{}
	if source.Forward != nil {
		original = source.Forward.OriginalSender
	} else {
		original.ID = source.SenderID
		if err = tx.QueryRow(ctx, "SELECT display_name FROM chat.users WHERE project_id=$1 AND id=$2 FOR SHARE", a.ProjectID, source.SenderID).Scan(&original.DisplayName); err != nil {
			return message.Sent{}, err
		}
	}
	forward := &message.Forward{MessageID: source.ID, OriginalSender: original}
	searchVersion, tokens, err := s.Crypto.Index(a.ProjectID, source.Content.Text)
	if errors.Is(err, cryptography.ErrTokens) {
		return message.Sent{}, policy.ErrInvalid
	}
	if err != nil {
		return message.Sent{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return message.Sent{}, err
	}
	payload := message.Payload{Content: *source.Content, Metadata: map[string]any{}, Forward: forward}
	plain, err := json.Marshal(payload)
	if err != nil {
		return message.Sent{}, err
	}
	envelope, err := s.Crypto.Seal(a.ProjectID, target, id.String(), plain)
	if err != nil {
		return message.Sent{}, err
	}
	var sequence int64
	if err = tx.QueryRow(ctx, "UPDATE chat.conversations SET message_sequence=message_sequence+1,updated_at=clock_timestamp() WHERE project_id=$1 AND id=$2 RETURNING message_sequence", a.ProjectID, target).Scan(&sequence); err != nil {
		return message.Sent{}, err
	}
	expires := time.Now().UTC().Add(time.Duration(retention) * 24 * time.Hour)
	if _, err = tx.Exec(ctx, `INSERT INTO chat.messages(id,project_id,conversation_id,sender_id,type,encrypted_content,nonce,key_version,payload_version,sequence,expires_at,forwarded_from_message_id)
 VALUES($1,$2,$3,$4,'TEXT',$5,$6,$7,$8,$9,$10,$11)`, id.String(), a.ProjectID, target, a.User.ID, envelope.Ciphertext, envelope.Nonce, envelope.KeyVersion, envelope.PayloadVersion, sequence, expires, source.ID); err != nil {
		return message.Sent{}, err
	}
	if _, err = tx.Exec(ctx, "UPDATE chat.conversations SET last_message_id=$3 WHERE project_id=$1 AND id=$2", a.ProjectID, target, id.String()); err != nil {
		return message.Sent{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO chat.message_search(project_id,conversation_id,message_id,search_key_version,tokens) VALUES($1,$2,$3,$4,$5)", a.ProjectID, target, id.String(), searchVersion, tokens); err != nil {
		return message.Sent{}, err
	}
	fingerprintVersion = s.Crypto.FingerprintVersion()
	fingerprint, err = s.Crypto.Fingerprint(a.ProjectID, fingerprintVersion, canonical)
	if err != nil {
		return message.Sent{}, err
	}
	dedupExpires = time.Now().UTC().Add(365 * 24 * time.Hour)
	if _, err = tx.Exec(ctx, "INSERT INTO chat.message_idempotency(project_id,sender_id,client_message_id,message_id,conversation_id,fingerprint,fingerprint_version,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", a.ProjectID, a.User.ID, command.ClientID, id.String(), target, fingerprint, fingerprintVersion, dedupExpires); err != nil {
		return message.Sent{}, err
	}
	if _, err = eventrepo.Append(ctx, tx, a.ProjectID, target, "message.created", map[string]any{"message_id": id.String(), "message_sequence": fmt.Sprint(sequence), "resource_version": "1", "sender_id": a.User.ID, "type": "TEXT", "forwarded": true}); err != nil {
		return message.Sent{}, err
	}
	m, err := s.scan(a.ProjectID, tx.QueryRow(ctx, "SELECT "+columns+" FROM chat.messages m WHERE m.project_id=$1 AND m.id=$2", a.ProjectID, id.String()))
	if err != nil {
		return message.Sent{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return message.Sent{}, err
	}
	return message.Sent{Message: m, Status: "SENT", DedupExpiresAt: dedupExpires}, nil
}
