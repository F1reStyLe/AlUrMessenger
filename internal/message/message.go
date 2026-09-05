// Package message defines encrypted message commands and authorized history/search.
package message

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/F1reStyLe/AlUrMessenger/internal/conversation"
	"github.com/F1reStyLe/AlUrMessenger/internal/cryptography"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
)

// ErrIdempotencyConflict rejects a reused client ID with different command content.
var ErrIdempotencyConflict = errors.New("IDEMPOTENCY_CONFLICT")

// Content is encrypted together with metadata; it is not stored in search/outbox JSON.
type Content struct {
	Text string `json:"text"`
}
type Payload struct {
	Content  Content        `json:"content"`
	Metadata map[string]any `json:"metadata"`
}

// Send includes no actor identity. HTTP only permits TEXT; SYSTEM is an internal command.
type Send struct {
	ClientID string         `json:"client_message_id"`
	Type     string         `json:"type"`
	Content  Content        `json:"content"`
	Metadata map[string]any `json:"metadata,omitempty"`
	ReplyTo  *string        `json:"reply_to_message_id,omitempty"`
}

// Validate bounds both plaintext and metadata before crypto or database work.
func (p Send) Validate(system bool) error {
	if p.ReplyTo != nil && (system || !conversation.ValidID(*p.ReplyTo)) {
		return policy.ErrInvalid
	}
	if !conversation.ValidID(p.ClientID) || (!system && p.Type != "TEXT") || (system && p.Type != "SYSTEM") || !utf8.ValidString(p.Content.Text) || len(p.Content.Text) > 16384 || strings.TrimSpace(p.Content.Text) == "" {
		return policy.ErrInvalid
	}
	data, err := json.Marshal(p.Metadata)
	if err != nil || len(data) > 8192 {
		return policy.ErrInvalid
	}
	return nil
}

// Canonical has stable map ordering and treats omitted/empty metadata equally.
func (p Send) Canonical(conversationID string) ([]byte, error) {
	if p.Metadata == nil {
		p.Metadata = map[string]any{}
	}
	return json.Marshal(struct {
		ConversationID string `json:"conversation_id"`
		Send
	}{conversationID, p})
}

// Reply is a same-conversation reference, never a copy of the original body.
// Clients resolve it through authorized Get; deletion cannot leave a stale preview.
type Reply struct {
	MessageID string `json:"message_id"`
}

// Edit replaces text only. Metadata, reply target, sender and sequence are immutable.
// Version is mandatory and represented as a decimal string for JavaScript clients.
type Edit struct {
	Content         Content `json:"content"`
	ExpectedVersion int64   `json:"expected_version,string"`
}

func (p Edit) Validate() error {
	if p.ExpectedVersion < 1 || !utf8.ValidString(p.Content.Text) || len(p.Content.Text) > 16384 || strings.TrimSpace(p.Content.Text) == "" {
		return policy.ErrInvalid
	}
	return nil
}

// Message is decrypted only after current Project/membership authorization.
// Tombstones omit all body fields and do not require an encryption key to read.
type Message struct {
	ID             string         `json:"id"`
	ConversationID string         `json:"conversation_id"`
	SenderID       string         `json:"sender_id"`
	Type           string         `json:"type"`
	Content        *Content       `json:"content,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	Reply          *Reply         `json:"reply,omitempty"`
	Status         string         `json:"status"`
	Sequence       int64          `json:"sequence,string"`
	Version        int64          `json:"resource_version,string"`
	CreatedAt      time.Time      `json:"created_at"`
	ExpiresAt      time.Time      `json:"expires_at"`
	EditedAt       *time.Time     `json:"edited_at,omitempty"`
	DeletedAt      *time.Time     `json:"deleted_at,omitempty"`
	Reactions      []Reaction     `json:"reactions"`
	Pin            *Pin           `json:"pin,omitempty"`
}

// Sent is returned only after commit; the same result identity survives retries.
type Sent struct {
	Message        Message   `json:"message"`
	Status         string    `json:"status"`
	Deduplicated   bool      `json:"deduplicated"`
	DedupExpiresAt time.Time `json:"dedup_expires_at"`
}

// Crypto keeps implementation and key storage outside message use cases.
type Crypto interface {
	Seal(string, string, string, []byte) (cryptography.Envelope, error)
	Open(string, string, string, cryptography.Envelope) ([]byte, error)
	Index(string, string) (string, []string, error)
	Queries(string, string) (map[string][]string, error)
	FingerprintVersion() string
	Fingerprint(string, string, []byte) ([]byte, error)
}

// Query uses exclusive sequence bounds and a small, deterministic page size.
type Query struct {
	Before, After int64
	Limit         int
	Forward       bool
}

func (q Query) Validate() error {
	if q.Before < 0 || q.After < 0 || (q.Before > 0 && (q.After > 0 || q.Forward)) || q.Limit < 1 || q.Limit > 101 {
		return policy.ErrInvalid
	}
	return nil
}

// Store owns atomic sequencing, idempotency, encryption persistence and authorization.
type Store interface {
	Send(context.Context, identity.Actor, string, Send, bool) (Sent, error)
	Get(context.Context, identity.Actor, string) (Message, error)
	History(context.Context, identity.Actor, string, Query) ([]Message, error)
	Search(context.Context, identity.Actor, string, string, Query) ([]Message, error)
	Edit(context.Context, identity.Actor, string, string, Edit) (Message, error)
	Delete(context.Context, identity.Actor, string, string) (Message, error)
	React(context.Context, identity.Actor, string, string, string, bool) (Message, error)
	SetPin(context.Context, identity.Actor, string, string, bool) (Message, error)
	Pins(context.Context, identity.Actor, string) (PinList, error)
}

// Service is shared by REST and WebSocket; client transports cannot invoke SYSTEM.
type Service struct{ Store Store }

// Send accepts only human/bot TEXT commands, with actor kind checked again in the DB.
func (s *Service) Send(ctx context.Context, a identity.Actor, id string, p Send) (Sent, error) {
	if !conversation.ValidID(id) {
		return Sent{}, policy.ErrInvalid
	}
	if err := p.Validate(false); err != nil {
		return Sent{}, err
	}
	return s.Store.Send(ctx, a, id, p, false)
}

// SendSystem is only for trusted internal callers with a real Project system identity.
// No HTTP/WS route exposes this method or trusts a boolean from client payload.
func (s *Service) SendSystem(ctx context.Context, a identity.Actor, id string, p Send) (Sent, error) {
	if !conversation.ValidID(id) {
		return Sent{}, policy.ErrInvalid
	}
	if err := p.Validate(true); err != nil {
		return Sent{}, err
	}
	return s.Store.Send(ctx, a, id, p, true)
}

// Get hides inaccessible/foreign messages; retained expired/deleted rows are tombstones.
func (s *Service) Get(ctx context.Context, a identity.Actor, id string) (Message, error) {
	if !conversation.ValidID(id) {
		return Message{}, policy.ErrInvalid
	}
	return s.Store.Get(ctx, a, id)
}

// Edit uses an optional conversation scope: REST discovers it from the message,
// whereas WS must match the envelope before any mutation can commit.
func (s *Service) Edit(ctx context.Context, a identity.Actor, id, scope string, p Edit) (Message, error) {
	if !conversation.ValidID(id) || (scope != "" && !conversation.ValidID(scope)) {
		return Message{}, policy.ErrInvalid
	}
	if err := p.Validate(); err != nil {
		return Message{}, err
	}
	return s.Store.Edit(ctx, a, id, scope, p)
}

// Delete is idempotent after current authorization and allow_delete checks.
func (s *Service) Delete(ctx context.Context, a identity.Actor, id, scope string) (Message, error) {
	if !conversation.ValidID(id) || (scope != "" && !conversation.ValidID(scope)) {
		return Message{}, policy.ErrInvalid
	}
	return s.Store.Delete(ctx, a, id, scope)
}

// History returns authorized rows; transport reverses a backwards page for ASC display.
func (s *Service) History(ctx context.Context, a identity.Actor, id string, q Query) ([]Message, error) {
	if !conversation.ValidID(id) {
		return nil, policy.ErrInvalid
	}
	if err := q.Validate(); err != nil {
		return nil, err
	}
	return s.Store.History(ctx, a, id, q)
}

// Search is whole-word AND matching, bounded to eight normalized words.
func (s *Service) Search(ctx context.Context, a identity.Actor, id, text string, q Query) ([]Message, error) {
	if !conversation.ValidID(id) || len(text) > 16384 || q.After != 0 || q.Forward {
		return nil, policy.ErrInvalid
	}
	if err := q.Validate(); err != nil {
		return nil, err
	}
	return s.Store.Search(ctx, a, id, text, q)
}
