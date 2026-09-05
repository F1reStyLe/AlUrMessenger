package moderation

import (
	"context"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/google/uuid"
)

// BanState is an admin-only projection. Public identity DTOs deliberately omit
// the reason and ban timestamp while still enforcing the resulting read-only mode.
type BanState struct {
	UserID   string     `json:"user_id"`
	Banned   bool       `json:"banned"`
	Reason   string     `json:"reason,omitempty"`
	Version  int64      `json:"policy_version,string"`
	BannedAt *time.Time `json:"banned_at,omitempty"`
}

type BanCommand struct {
	Reason          string `json:"reason"`
	ExpectedVersion int64  `json:"expected_version,string"`
}

func (c BanCommand) Validate() error {
	if c.ExpectedVersion < 1 || !utf8.ValidString(c.Reason) || strings.TrimSpace(c.Reason) != c.Reason || c.Reason == "" || utf8.RuneCountInString(c.Reason) > 1000 || strings.IndexFunc(c.Reason, unicode.IsControl) >= 0 {
		return policy.ErrInvalid
	}
	return nil
}

type UnbanCommand struct {
	ExpectedVersion int64 `json:"expected_version,string"`
}

func (c UnbanCommand) Validate() error {
	if c.ExpectedVersion < 1 {
		return policy.ErrInvalid
	}
	return nil
}

type BanStore interface {
	Ban(context.Context, identity.Actor, string, BanCommand, policy.Trace) (BanState, error)
	Unban(context.Context, identity.Actor, string, UnbanCommand, policy.Trace) (BanState, error)
}

type BanService struct{ Store BanStore }

func (s *BanService) Ban(ctx context.Context, actor identity.Actor, user string, command BanCommand, trace policy.Trace) (BanState, error) {
	if err := admin(actor); err != nil {
		return BanState{}, err
	}
	if uuid.Validate(user) != nil || user == actor.User.ID || command.Validate() != nil {
		return BanState{}, policy.ErrInvalid
	}
	return s.Store.Ban(ctx, actor, user, command, trace)
}

func (s *BanService) Unban(ctx context.Context, actor identity.Actor, user string, command UnbanCommand, trace policy.Trace) (BanState, error) {
	if err := admin(actor); err != nil {
		return BanState{}, err
	}
	if uuid.Validate(user) != nil || user == actor.User.ID || command.Validate() != nil {
		return BanState{}, policy.ErrInvalid
	}
	return s.Store.Unban(ctx, actor, user, command, trace)
}
