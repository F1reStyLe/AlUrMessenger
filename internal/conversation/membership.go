package conversation

import (
	"context"
	"errors"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
)

// ErrLastModerator protects a live human able to administer a private conversation.
var ErrLastModerator = errors.New("LAST_MODERATOR_REQUIRED")

// Member is a scoped membership projection, including a left result after removal.
type Member struct {
	// Other members' checkpoints are intentionally absent from the directory;
	// read-receipt privacy is applied by the replay path, not bypassed by this DTO.
	UserID   string     `json:"user_id"`
	Role     string     `json:"role"`
	JoinedAt time.Time  `json:"joined_at"`
	Banned   bool       `json:"banned"`
	Version  int64      `json:"version,string"`
	LeftAt   *time.Time `json:"left_at"`
	Muted    bool       `json:"muted"`
}

// AddMembers supports a bounded batch, with no role assignment from client payload.
type AddMembers struct {
	UserIDs []string `json:"user_ids"`
}

// Validate rejects duplicates and malformed IDs before obtaining database locks.
func (p AddMembers) Validate() error {
	if len(p.UserIDs) == 0 || len(p.UserIDs) > 100 {
		return policy.ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range p.UserIDs {
		if !ValidID(id) || seen[id] {
			return policy.ErrInvalid
		}
		seen[id] = true
	}
	return nil
}

// MemberPatch keeps personal notification preference separate from moderator powers.
type MemberPatch struct {
	Role            *string `json:"role"`
	Banned          *bool   `json:"banned"`
	Muted           *bool   `json:"muted"`
	ExpectedVersion int64   `json:"expected_version,string"`
}

// Validate prevents unknown roles, empty updates and unversioned writes.
func (p MemberPatch) Validate() error {
	if p.ExpectedVersion < 1 || (p.Role == nil && p.Banned == nil && p.Muted == nil) || (p.Role != nil && *p.Role != "member" && *p.Role != "moderator") {
		return policy.ErrInvalid
	}
	return nil
}

// MembershipStore is explicit so conversation-only callers need not fake role management.
type MembershipStore interface {
	Members(context.Context, identity.Actor, string, string, int) ([]Member, error)
	AddMembers(context.Context, identity.Actor, string, AddMembers, policy.Trace) ([]Member, error)
	PatchMember(context.Context, identity.Actor, string, string, MemberPatch, policy.Trace) (Member, error)
	RemoveMember(context.Context, identity.Actor, string, string, policy.Trace) error
}

// MembershipService provides request validation before the locked storage policy.
type MembershipService struct{ Store MembershipStore }

// Members returns active members only, including banned members who retain read access.
func (s *MembershipService) Members(ctx context.Context, a identity.Actor, id, after string, limit int) ([]Member, error) {
	if !ValidID(id) || limit < 1 || limit > 101 || (after != "" && !ValidID(after)) {
		return nil, policy.ErrInvalid
	}
	return s.Store.Members(ctx, a, id, after, limit)
}

// Add never gives an invited user moderator privileges; promotion is a separate audited action.
func (s *MembershipService) Add(ctx context.Context, a identity.Actor, id string, p AddMembers, tr policy.Trace) ([]Member, error) {
	if !ValidID(id) {
		return nil, policy.ErrInvalid
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return s.Store.AddMembers(ctx, a, id, p, tr)
}

// Patch applies self-mute or moderator role/ban changes under the same version contract.
func (s *MembershipService) Patch(ctx context.Context, a identity.Actor, id, user string, p MemberPatch, tr policy.Trace) (Member, error) {
	if !ValidID(id) || !ValidID(user) {
		return Member{}, policy.ErrInvalid
	}
	if err := p.Validate(); err != nil {
		return Member{}, err
	}
	return s.Store.PatchMember(ctx, a, id, user, p, tr)
}

// Remove leaves history intact. DIRECT leave is unsupported; last moderator cannot leave.
func (s *MembershipService) Remove(ctx context.Context, a identity.Actor, id, user string, tr policy.Trace) error {
	if !ValidID(id) || !ValidID(user) {
		return policy.ErrInvalid
	}
	return s.Store.RemoveMember(ctx, a, id, user, tr)
}
