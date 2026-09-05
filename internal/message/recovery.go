package message

import (
	"context"
	"errors"

	"github.com/F1reStyLe/AlUrMessenger/internal/conversation"
	"github.com/F1reStyLe/AlUrMessenger/internal/event"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
)

// ErrResync means an invalid cursor or a pruned/gapped changefeed requires a snapshot.
var ErrResync = errors.New("RESYNC_REQUIRED")

// Checkpoint is user-scoped, shared by devices; socket write is never a receipt.
type Checkpoint struct {
	UserID    string `json:"user_id"`
	Read      int64  `json:"last_read_sequence,string"`
	Delivered int64  `json:"last_delivered_sequence,string"`
	Version   int64  `json:"member_version,string"`
}

// Replay carries a stable high watermark and the last contiguous processed event.
type Replay struct {
	Events  []event.Envelope `json:"events"`
	Through int64            `json:"through_event_sequence,string"`
	High    int64            `json:"high_watermark,string"`
	More    bool             `json:"has_more"`
}

// Snapshot is one DB snapshot, not a composition of independently changing GETs.
// Pins includes every live pinned message ID, including messages outside recent history.
type Snapshot struct {
	Conversation    conversation.Conversation `json:"conversation"`
	Checkpoint      Checkpoint                `json:"checkpoint"`
	Messages        []Message                 `json:"messages"`
	Pins            []string                  `json:"pins"`
	EventSequence   int64                     `json:"snapshot_event_sequence,string"`
	MessageSequence int64                     `json:"message_sequence,string"`
}

// RecoveryStore separates persisted delivery/replay from any network transport.
type RecoveryStore interface {
	Replay(context.Context, identity.Actor, string, int64, int) (Replay, error)
	Snapshot(context.Context, identity.Actor, string) (Snapshot, error)
	Receipt(context.Context, identity.Actor, string, string, int64) (Checkpoint, error)
}
type RecoveryService struct{ Store RecoveryStore }

// Replay rejects invalid bounds before repository access.
func (s *RecoveryService) Replay(ctx context.Context, a identity.Actor, id string, after int64, limit int) (Replay, error) {
	if !conversation.ValidID(id) || after < 0 || limit < 1 || limit > 100 {
		return Replay{}, policy.ErrInvalid
	}
	return s.Store.Replay(ctx, a, id, after, limit)
}

// Snapshot is used for first subscription and RESYNC_REQUIRED recovery.
func (s *RecoveryService) Snapshot(ctx context.Context, a identity.Actor, id string) (Snapshot, error) {
	if !conversation.ValidID(id) {
		return Snapshot{}, policy.ErrInvalid
	}
	return s.Store.Snapshot(ctx, a, id)
}

// Receipt changes only the authenticated actor's monotonic checkpoint.
func (s *RecoveryService) Receipt(ctx context.Context, a identity.Actor, id, kind string, sequence int64) (Checkpoint, error) {
	if !conversation.ValidID(id) || sequence < 0 || (kind != "read" && kind != "delivered") {
		return Checkpoint{}, policy.ErrInvalid
	}
	return s.Store.Receipt(ctx, a, id, kind, sequence)
}
