// Package repository implements atomic message persistence and current authorization.
package repository

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/internal/attachment"
	"github.com/F1reStyLe/AlUrMessenger/internal/auth"
	"github.com/F1reStyLe/AlUrMessenger/internal/cryptography"
	eventrepo "github.com/F1reStyLe/AlUrMessenger/internal/event/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/message"
	moderationrepo "github.com/F1reStyLe/AlUrMessenger/internal/moderation/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store borrows runtime SQL and an immutable versioned crypto provider.
type Store struct {
	DB     *pgxpool.Pool
	Crypto message.Crypto
}

// columns contains ciphertext only; the decoder runs after an authorized lookup.
const columns = `m.id::text,m.conversation_id::text,m.sender_id::text,m.type,m.sequence,m.resource_version,m.created_at,m.expires_at,m.encrypted_content,m.nonce,m.key_version,m.payload_version,m.reply_to_message_id::text,m.forwarded_from_message_id::text,m.edited_at,m.deleted_at,m.expires_at<=clock_timestamp(),
 COALESCE((SELECT jsonb_agg(r ORDER BY r.reaction) FROM
 (SELECT reaction,count(*)::text AS count FROM chat.message_reactions WHERE project_id=m.project_id AND conversation_id=m.conversation_id AND message_id=m.id GROUP BY reaction) r),'[]'::jsonb),
 (SELECT jsonb_build_object('message_id',p.message_id,'pinned_by',p.pinned_by,'created_at',p.created_at,'resource_version',m.resource_version::text)
 FROM chat.pinned_messages p WHERE p.project_id=m.project_id AND p.conversation_id=m.conversation_id AND p.message_id=m.id),
 COALESCE((SELECT jsonb_agg(jsonb_build_object('id',a.id,'original_name',a.original_name,'mime_type',a.mime_type,'size',a.size,'width',a.width,'height',a.height,'status',a.status,'created_at',a.created_at,'expires_at',a.expires_at) ORDER BY ma.position)
 FROM chat.message_attachments ma JOIN chat.attachments a ON a.project_id=ma.project_id AND a.id=ma.attachment_id
 WHERE ma.project_id=m.project_id AND ma.conversation_id=m.conversation_id AND ma.message_id=m.id),'[]'::jsonb)`

// scan decrypts the exact row context. Corrupt/unknown key versions fail closed.
func (s *Store) scan(project string, row pgx.Row) (message.Message, error) {
	var m message.Message
	var e cryptography.Envelope
	var reply, forwarded *string
	var expired bool
	var reactions, pin, attachments []byte
	err := row.Scan(&m.ID, &m.ConversationID, &m.SenderID, &m.Type, &m.Sequence, &m.Version, &m.CreatedAt, &m.ExpiresAt, &e.Ciphertext, &e.Nonce, &e.KeyVersion, &e.PayloadVersion, &reply, &forwarded, &m.EditedAt, &m.DeletedAt, &expired, &reactions, &pin, &attachments)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, identity.ErrNotFound
	}
	if err != nil {
		return m, err
	}
	// Never decrypt a tombstone, even if old keys are gone or its wiped ciphertext
	// is no longer a valid GCM envelope. DB time determines retention eligibility.
	m.Status = "active"
	m.Reactions = []message.Reaction{}
	m.Attachments = []attachment.Attachment{}
	if m.DeletedAt != nil {
		m.Status = "deleted"
		return m, nil
	}
	if expired {
		m.Status = "expired"
		return m, nil
	}
	// The relation aggregates are part of this same SQL snapshot as the version.
	// Never expose them on terminal rows, even before physical retention cleanup.
	if err = json.Unmarshal(reactions, &m.Reactions); err != nil {
		return m, err
	}
	if len(pin) > 0 {
		if err = json.Unmarshal(pin, &m.Pin); err != nil {
			return m, err
		}
	}
	if err = json.Unmarshal(attachments, &m.Attachments); err != nil {
		return m, err
	}
	plain, err := s.Crypto.Open(project, m.ConversationID, m.ID, e)
	if err != nil {
		return m, err
	}
	var payload message.Payload
	dec := json.NewDecoder(strings.NewReader(string(plain)))
	dec.UseNumber()
	if dec.Decode(&payload) != nil {
		return m, cryptography.ErrCrypto
	}
	m.Content, m.Metadata = &payload.Content, payload.Metadata
	m.Forward = payload.Forward
	// The relational header is cleared when retention physically purges the
	// source, while the encrypted snapshot deliberately remains self-contained.
	// A present header must still agree with the authenticated payload.
	if (forwarded != nil && m.Forward == nil) || (forwarded != nil && *forwarded != m.Forward.MessageID) {
		return m, cryptography.ErrCrypto
	}
	if reply != nil {
		m.Reply = &message.Reply{MessageID: *reply}
	}
	return m, nil
}

// readAccess locks current membership through the query/decryption, ordering a
// concurrent leave after this already-authorized read. Bans preserve read-only access.
func readAccess(ctx context.Context, tx pgx.Tx, a identity.Actor, conversation string) error {
	var found string
	err := tx.QueryRow(ctx, `SELECT c.id::text FROM chat.conversations c JOIN chat.projects p ON p.id=c.project_id AND p.status='active'
 JOIN chat.conversation_members cm ON cm.project_id=c.project_id AND cm.conversation_id=c.id
 WHERE c.project_id=$1 AND c.id=$2 AND c.deleted_at IS NULL AND cm.user_id=$3 AND cm.left_at IS NULL FOR SHARE OF cm`, a.ProjectID, conversation, a.User.ID).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.ErrNotFound
	}
	return err
}

// writeAccess takes the documented Project → actor → idempotency → conversation →
// membership locks. SYSTEM is reserved for a DB-backed system actor and internal call.
func writeAccess(ctx context.Context, tx pgx.Tx, a identity.Actor, conversation, clientID string, system bool) (int, error) {
	return activityAccess(ctx, tx, a, conversation, clientID, system, true)
}

// activityAccess shares lock ordering and ban/identity checks with message writes.
// Reactions are available to CHANNEL readers too; only content writes require a
// moderator. Pins perform their stricter GROUP/CHANNEL role check after this gate.
func activityAccess(ctx context.Context, tx pgx.Tx, a identity.Actor, conversation, clientID string, system, channelWrite bool) (int, error) {
	var active, bots, banned bool
	var retention int
	var kind string
	err := tx.QueryRow(ctx, `SELECT p.status='active',COALESCE((s.flags->>'allow_bots')::boolean,false),s.message_retention_days
 FROM chat.projects p JOIN chat.project_settings s ON s.project_id=p.id WHERE p.id=$1 FOR SHARE OF p`, a.ProjectID).Scan(&active, &bots, &retention)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, auth.ErrUnauthenticated
	}
	if err != nil {
		return 0, err
	}
	if !active {
		return 0, auth.ErrUnauthenticated
	}
	err = tx.QueryRow(ctx, "SELECT kind,banned_at IS NOT NULL FROM chat.users WHERE project_id=$1 AND id=$2 FOR SHARE", a.ProjectID, a.User.ID).Scan(&kind, &banned)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, identity.ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if banned {
		return 0, identity.ErrBanned
	}
	if system {
		if kind != "system" {
			return 0, policy.ErrForbidden
		}
	} else {
		if kind != "human" && kind != "bot" {
			return 0, policy.ErrForbidden
		}
		if kind == "bot" && !bots {
			return 0, policy.ErrFeatureDisabled
		}
	}
	// Edits/deletes have no send receipt and go straight to the conversation lock.
	if clientID != "" {
		if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "message.dedup:"+a.ProjectID+":"+a.User.ID+":"+clientID); err != nil {
			return 0, err
		}
	}
	var conversationType string
	err = tx.QueryRow(ctx, "SELECT type FROM chat.conversations WHERE project_id=$1 AND id=$2 AND deleted_at IS NULL FOR UPDATE", a.ProjectID, conversation).Scan(&conversationType)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, identity.ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if !system {
		var role string
		err = tx.QueryRow(ctx, "SELECT role,banned FROM chat.conversation_members WHERE project_id=$1 AND conversation_id=$2 AND user_id=$3 AND left_at IS NULL FOR SHARE", a.ProjectID, conversation, a.User.ID).Scan(&role, &banned)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, identity.ErrNotFound
		}
		if err != nil {
			return 0, err
		}
		if banned {
			return 0, identity.ErrBanned
		}
		if channelWrite && conversationType == "CHANNEL" && role != "moderator" {
			return 0, policy.ErrForbidden
		}
	}
	return retention, nil
}

// Send commits message, search tokens, dedup receipt, sequence and outbox together.
// Retry first rechecks access and then compares a keyed canonical fingerprint.
func (s *Store) Send(ctx context.Context, a identity.Actor, conversation string, p message.Send, system bool) (message.Sent, error) {
	if err := p.Validate(system); err != nil {
		return message.Sent{}, err
	}
	if p.ForwardFrom != nil {
		return s.forward(ctx, a, conversation, p)
	}
	canonical, err := p.Canonical(conversation)
	if err != nil {
		return message.Sent{}, policy.ErrInvalid
	}
	searchText := p.Content.Text
	if p.Type == "IMAGE" {
		searchText = p.Content.Caption
	}
	searchVersion, tokens, err := s.Crypto.Index(a.ProjectID, searchText)
	if errors.Is(err, cryptography.ErrTokens) {
		return message.Sent{}, policy.ErrInvalid
	}
	if err != nil {
		return message.Sent{}, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return message.Sent{}, err
	}
	defer tx.Rollback(ctx)
	retention, err := writeAccess(ctx, tx, a, conversation, p.ClientID, system)
	if err != nil {
		return message.Sent{}, err
	}
	if p.ReplyTo != nil {
		if err = requireFeature(ctx, tx, a.ProjectID, "allow_reply"); err != nil {
			return message.Sent{}, err
		}
	}
	if p.Type == "IMAGE" {
		if err = requireFeature(ctx, tx, a.ProjectID, "allow_images"); err != nil {
			return message.Sent{}, err
		}
	}
	var existing, version string
	var fingerprint []byte
	var dedupExpires time.Time
	err = tx.QueryRow(ctx, "SELECT message_id::text,fingerprint_version,fingerprint,expires_at FROM chat.message_idempotency WHERE project_id=$1 AND sender_id=$2 AND client_message_id=$3", a.ProjectID, a.User.ID, p.ClientID).Scan(&existing, &version, &fingerprint, &dedupExpires)
	if err == nil && dedupExpires.After(time.Now()) {
		candidate, err := s.Crypto.Fingerprint(a.ProjectID, version, canonical)
		if err != nil {
			return message.Sent{}, err
		}
		if !hmac.Equal(candidate, fingerprint) {
			return message.Sent{}, message.ErrIdempotencyConflict
		}
		// A retry must not resurrect expired content even while its dedup receipt lives.
		m, err := s.scan(a.ProjectID, tx.QueryRow(ctx, "SELECT "+columns+" FROM chat.messages m WHERE m.project_id=$1 AND m.id=$2", a.ProjectID, existing))
		if err != nil {
			return message.Sent{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return message.Sent{}, err
		}
		return message.Sent{Message: m, Status: "SENT", Deduplicated: true, DedupExpiresAt: dedupExpires}, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return message.Sent{}, err
	}
	if existing != "" {
		if _, err = tx.Exec(ctx, "DELETE FROM chat.message_idempotency WHERE project_id=$1 AND sender_id=$2 AND client_message_id=$3", a.ProjectID, a.User.ID, p.ClientID); err != nil {
			return message.Sent{}, err
		}
	}
	content := p.Content.Text
	if p.Type == "IMAGE" {
		content = p.Content.Caption
	}
	if err = moderationrepo.CheckContent(ctx, tx, a.ProjectID, content); err != nil {
		return message.Sent{}, err
	}
	// A new IMAGE consumes one ready logical attachment under the same
	// transaction. This check intentionally follows the dedup receipt path: an
	// exact retry observes the already-attached object instead of conflicting.
	if p.Type == "IMAGE" {
		var ready string
		err = tx.QueryRow(ctx, `SELECT a.id::text FROM chat.attachments a JOIN chat.storage_objects o ON o.project_id=a.project_id AND o.id=a.object_id
 WHERE a.project_id=$1 AND a.id=$2 AND a.uploader_id=$3 AND a.status='ready' AND a.expires_at>clock_timestamp() AND o.status='ready'
 FOR UPDATE OF a,o`, a.ProjectID, p.AttachmentIDs[0], a.User.ID).Scan(&ready)
		if errors.Is(err, pgx.ErrNoRows) {
			return message.Sent{}, identity.ErrNotFound
		}
		if err != nil {
			return message.Sent{}, err
		}
	}
	// Only a new send needs a live reply target. A matching retry still returns
	// its committed result after the parent was deleted, without creating a copy.
	if p.ReplyTo != nil {
		var found string
		err = tx.QueryRow(ctx, "SELECT id::text FROM chat.messages WHERE project_id=$1 AND conversation_id=$2 AND id=$3 AND deleted_at IS NULL AND expires_at>clock_timestamp()", a.ProjectID, conversation, *p.ReplyTo).Scan(&found)
		if errors.Is(err, pgx.ErrNoRows) {
			return message.Sent{}, identity.ErrNotFound
		}
		if err != nil {
			return message.Sent{}, err
		}
	}
	id, err := uuid.NewV7()
	if err != nil {
		return message.Sent{}, err
	}
	if p.Metadata == nil {
		p.Metadata = map[string]any{}
	}
	plain, err := json.Marshal(message.Payload{Content: p.Content, Metadata: p.Metadata})
	if err != nil {
		return message.Sent{}, policy.ErrInvalid
	}
	envelope, err := s.Crypto.Seal(a.ProjectID, conversation, id.String(), plain)
	if err != nil {
		return message.Sent{}, err
	}
	var sequence int64
	if err = tx.QueryRow(ctx, "UPDATE chat.conversations SET message_sequence=message_sequence+1,updated_at=clock_timestamp() WHERE project_id=$1 AND id=$2 RETURNING message_sequence", a.ProjectID, conversation).Scan(&sequence); err != nil {
		return message.Sent{}, err
	}
	expires := time.Now().UTC().Add(time.Duration(retention) * 24 * time.Hour)
	if _, err = tx.Exec(ctx, `INSERT INTO chat.messages(id,project_id,conversation_id,sender_id,type,encrypted_content,nonce,key_version,payload_version,sequence,expires_at,reply_to_message_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, id.String(), a.ProjectID, conversation, a.User.ID, p.Type, envelope.Ciphertext, envelope.Nonce, envelope.KeyVersion, envelope.PayloadVersion, sequence, expires, p.ReplyTo); err != nil {
		return message.Sent{}, err
	}
	if p.Type == "IMAGE" {
		if _, err = tx.Exec(ctx, "INSERT INTO chat.message_attachments(project_id,conversation_id,message_id,attachment_id) VALUES($1,$2,$3,$4)", a.ProjectID, conversation, id.String(), p.AttachmentIDs[0]); err != nil {
			return message.Sent{}, err
		}
		if _, err = tx.Exec(ctx, "UPDATE chat.attachments SET status='attached' WHERE project_id=$1 AND id=$2 AND status='ready'", a.ProjectID, p.AttachmentIDs[0]); err != nil {
			return message.Sent{}, err
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE chat.conversations SET last_message_id=$3 WHERE project_id=$1 AND id=$2", a.ProjectID, conversation, id.String()); err != nil {
		return message.Sent{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO chat.message_search(project_id,conversation_id,message_id,search_key_version,tokens) VALUES($1,$2,$3,$4,$5)", a.ProjectID, conversation, id.String(), searchVersion, tokens); err != nil {
		return message.Sent{}, err
	}
	version = s.Crypto.FingerprintVersion()
	fingerprint, err = s.Crypto.Fingerprint(a.ProjectID, version, canonical)
	if err != nil {
		return message.Sent{}, err
	}
	dedupExpires = time.Now().UTC().Add(365 * 24 * time.Hour)
	if _, err = tx.Exec(ctx, "INSERT INTO chat.message_idempotency(project_id,sender_id,client_message_id,message_id,conversation_id,fingerprint,fingerprint_version,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", a.ProjectID, a.User.ID, p.ClientID, id.String(), conversation, fingerprint, version, dedupExpires); err != nil {
		return message.Sent{}, err
	}
	if _, err = eventrepo.Append(ctx, tx, a.ProjectID, conversation, "message.created", map[string]any{"message_id": id.String(), "message_sequence": fmt.Sprint(sequence), "resource_version": "1", "sender_id": a.User.ID, "type": p.Type}); err != nil {
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

// Get performs tenant discovery and membership locking before payload decryption.
func (s *Store) Get(ctx context.Context, a identity.Actor, id string) (message.Message, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return message.Message{}, err
	}
	defer tx.Rollback(ctx)
	var conversation string
	err = tx.QueryRow(ctx, "SELECT conversation_id::text FROM chat.messages WHERE project_id=$1 AND id=$2", a.ProjectID, id).Scan(&conversation)
	if errors.Is(err, pgx.ErrNoRows) {
		return message.Message{}, identity.ErrNotFound
	}
	if err != nil {
		return message.Message{}, err
	}
	if err = readAccess(ctx, tx, a, conversation); err != nil {
		return message.Message{}, err
	}
	m, err := s.scan(a.ProjectID, tx.QueryRow(ctx, "SELECT "+columns+" FROM chat.messages m WHERE m.project_id=$1 AND m.id=$2", a.ProjectID, id))
	if err != nil {
		return m, err
	}
	return m, tx.Commit(ctx)
}

// page includes retained tombstones in history but only live matches in search.
// Caller input supplies parameters, never SQL fragments.
func (s *Store) page(ctx context.Context, a identity.Actor, conversation string, q message.Query, search map[string][]string) ([]message.Message, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = readAccess(ctx, tx, a, conversation); err != nil {
		return nil, err
	}
	bound := q.Before
	direction, comparison := "DESC", "<"
	if bound == 0 {
		bound = math.MaxInt64
	}
	if q.After > 0 || q.Forward {
		bound = q.After
		direction, comparison = "ASC", ">"
	}
	args := []any{a.ProjectID, conversation, bound, q.Limit}
	query := "SELECT " + columns + " FROM chat.messages m WHERE m.project_id=$1 AND m.conversation_id=$2 AND m.sequence" + comparison + "$3"
	if search != nil {
		query += " AND m.deleted_at IS NULL AND m.expires_at>clock_timestamp()"
		query += " AND EXISTS(SELECT 1 FROM chat.message_search s WHERE s.project_id=m.project_id AND s.conversation_id=m.conversation_id AND s.message_id=m.id AND ("
		parts := []string{}
		for version, tokens := range search {
			args = append(args, version, tokens)
			parts = append(parts, fmt.Sprintf("(s.search_key_version=$%d AND s.tokens @> $%d::text[])", len(args)-1, len(args)))
		}
		query += strings.Join(parts, " OR ") + "))"
	}
	query += " ORDER BY m.sequence " + direction + " LIMIT $4"
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	items := []message.Message{}
	for rows.Next() {
		m, e := s.scan(a.ProjectID, rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		items = append(items, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return items, tx.Commit(ctx)
}

// History returns backwards pages in DESC storage order and forwards pages in ASC.
func (s *Store) History(ctx context.Context, a identity.Actor, conversation string, q message.Query) ([]message.Message, error) {
	return s.page(ctx, a, conversation, q, nil)
}

// Search computes all retained blind-key versions and applies AND tokens within each.
func (s *Store) Search(ctx context.Context, a identity.Actor, conversation, text string, q message.Query) ([]message.Message, error) {
	queries, err := s.Crypto.Queries(a.ProjectID, text)
	if errors.Is(err, cryptography.ErrTokens) {
		return nil, policy.ErrInvalid
	}
	if err != nil {
		return nil, err
	}
	return s.page(ctx, a, conversation, q, queries)
}
