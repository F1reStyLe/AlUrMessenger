package message

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/F1reStyLe/AlUrMessenger/internal/conversation"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
)

// Reaction is a bounded aggregate, not an unbounded list of reacting users.
// Counts are decimal strings, as are all potentially large public counters.
type Reaction struct {
	Emoji string `json:"reaction"`
	Count int64  `json:"count,string"`
}

// Pin carries a reference only; clients obtain content through authorized Get.
// Version is the current message version, not the time at which it was pinned.
type Pin struct {
	MessageID string    `json:"message_id"`
	PinnedBy  string    `json:"pinned_by"`
	CreatedAt time.Time `json:"created_at"`
	Version   int64     `json:"resource_version,string"`
}

// PinList uses the same consistent event watermark as recovery. At most 100
// live pins may exist per conversation, so replacing this collection is bounded.
type PinList struct {
	Items         []Pin `json:"items"`
	EventSequence int64 `json:"snapshot_event_sequence,string"`
}

// NormalizeReaction accepts one Unicode Emoji 16.0 sequence from the pinned
// official data, including its listed presentation aliases. A rune/range check
// would accept broken ZWJ sequences and reject valid families, flags and keycaps.
func NormalizeReaction(value string) (string, error) {
	if len(value) > 128 || !utf8.ValidString(value) {
		return "", policy.ErrInvalid
	}
	canonical, ok := reactionEmoji[value]
	if !ok {
		return "", policy.ErrInvalid
	}
	return canonical, nil
}

// React always changes the authenticated actor's relation. Scope is required by
// WS and optional for the REST item route; the repository checks it before writes.
func (s *Service) React(ctx context.Context, a identity.Actor, id, scope, emoji string, add bool) (Message, error) {
	if !conversation.ValidID(id) || (scope != "" && !conversation.ValidID(scope)) {
		return Message{}, policy.ErrInvalid
	}
	canonical, err := NormalizeReaction(emoji)
	if err != nil {
		return Message{}, err
	}
	return s.Store.React(ctx, a, id, scope, canonical, add)
}

// SetPin requires a conversation even on REST, and never trusts a client pin owner.
func (s *Service) SetPin(ctx context.Context, a identity.Actor, conversationID, id string, add bool) (Message, error) {
	if !conversation.ValidID(id) || !conversation.ValidID(conversationID) {
		return Message{}, policy.ErrInvalid
	}
	return s.Store.SetPin(ctx, a, id, conversationID, add)
}

func (s *Service) Pins(ctx context.Context, a identity.Actor, id string) (PinList, error) {
	if !conversation.ValidID(id) {
		return PinList{}, policy.ErrInvalid
	}
	return s.Store.Pins(ctx, a, id)
}
