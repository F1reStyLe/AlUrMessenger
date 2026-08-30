// Package conversation defines private DIRECT/GROUP/CHANNEL use cases. Auth and
// Project roles prove the actor but never replace conversation membership.
package conversation

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/google/uuid"
)

// ErrUnsupported distinguishes an immutable DIRECT operation from bad JSON.
var ErrUnsupported = errors.New("CONVERSATION_OPERATION_UNSUPPORTED")

// Membership exposes only the caller's state, not an implicit directory of members.
type Membership struct {
	UserID    string    `json:"user_id"`
	Role      string    `json:"role"`
	JoinedAt  time.Time `json:"joined_at"`
	Banned    bool      `json:"banned"`
	Version   int64     `json:"version,string"`
	Read      int64     `json:"last_read_sequence,string"`
	Delivered int64     `json:"last_delivered_sequence,string"`
}

// Conversation exposes message and event cursors separately; last_message_id is a
// reference whose content must still pass current expiration/access checks.
type Conversation struct {
	ID              string     `json:"id"`
	Type            string     `json:"type"`
	Title           string     `json:"title"`
	AvatarURL       string     `json:"avatar_url"`
	CreatedBy       string     `json:"created_by"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	Version         int64      `json:"resource_version,string"`
	Membership      Membership `json:"own_membership"`
	MessageSequence int64      `json:"message_sequence,string"`
	EventSequence   int64      `json:"event_sequence,string"`
	LastMessageID   *string    `json:"last_message_id"`
}

// Create names only other, already provisioned users in the actor's Project.
// Creator is implicit; 100 invitees bound row locks and transaction work per request.
type Create struct {
	Type      string   `json:"type"`
	MemberIDs []string `json:"member_ids"`
	Title     string   `json:"title"`
	AvatarURL string   `json:"avatar_url"`
}

// ValidID rejects nil/noncanonical UUIDs before SQL and before cursor comparisons.
func ValidID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

// validTitle allows an unnamed conversation but rejects misleading whitespace/control characters.
func validTitle(value string) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= 128 && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) == -1
}

// validAvatar permits clearing the value; external URLs are never fetched by Chat.
func validAvatar(value string) bool {
	if value == "" {
		return true
	}
	u, err := url.Parse(value)
	return err == nil && len(value) <= 2048 && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}

// Validate rejects self/duplicate invitees, unknown types and DIRECT metadata.
func (p Create) Validate(actorID string) error {
	if !ValidID(actorID) || (p.Type != "DIRECT" && p.Type != "GROUP" && p.Type != "CHANNEL") || len(p.MemberIDs) > 100 || !validTitle(p.Title) || !validAvatar(p.AvatarURL) {
		return policy.ErrInvalid
	}
	if p.Type == "DIRECT" && (len(p.MemberIDs) != 1 || p.Title != "" || p.AvatarURL != "") {
		return policy.ErrInvalid
	}
	seen := map[string]bool{actorID: true}
	for _, id := range p.MemberIDs {
		if !ValidID(id) || seen[id] {
			return policy.ErrInvalid
		}
		seen[id] = true
	}
	return nil
}

// Patch is versioned and cannot change type, creator, Project or members.
type Patch struct {
	Title           *string `json:"title"`
	AvatarURL       *string `json:"avatar_url"`
	ExpectedVersion int64   `json:"expected_version,string"`
}

// Validate permits empty strings to clear fields but requires at least one change.
func (p Patch) Validate() error {
	if p.ExpectedVersion < 1 || (p.Title == nil && p.AvatarURL == nil) || (p.Title != nil && !validTitle(*p.Title)) || (p.AvatarURL != nil && !validAvatar(*p.AvatarURL)) {
		return policy.ErrInvalid
	}
	return nil
}

// Store must enforce tenant and membership predicates even when called without HTTP.
type Store interface {
	Create(context.Context, identity.Actor, Create, policy.Trace) (Conversation, bool, error)
	Get(context.Context, identity.Actor, string) (Conversation, error)
	List(context.Context, identity.Actor, string, int) ([]Conversation, error)
	Patch(context.Context, identity.Actor, string, Patch, policy.Trace) (Conversation, error)
}

// Service validates request bounds before DB access; repository repeats write checks under locks.
type Service struct{ Store Store }

// Create returns created=false only for an existing, accessible canonical DIRECT.
func (s *Service) Create(ctx context.Context, a identity.Actor, p Create, tr policy.Trace) (Conversation, bool, error) {
	if a.User.Banned {
		return Conversation{}, false, identity.ErrBanned
	}
	if a.User.Kind != "human" {
		return Conversation{}, false, policy.ErrForbidden
	}
	if err := p.Validate(a.User.ID); err != nil {
		return Conversation{}, false, err
	}
	return s.Store.Create(ctx, a, p, tr)
}

// Get deliberately gives administrators no membership bypass.
func (s *Service) Get(ctx context.Context, a identity.Actor, id string) (Conversation, error) {
	if !ValidID(id) {
		return Conversation{}, policy.ErrInvalid
	}
	return s.Store.Get(ctx, a, id)
}

// List has a bounded forward UUID cursor; the repository always filters active membership.
func (s *Service) List(ctx context.Context, a identity.Actor, after string, limit int) ([]Conversation, error) {
	if limit < 1 || limit > 101 || (after != "" && !ValidID(after)) {
		return nil, policy.ErrInvalid
	}
	return s.Store.List(ctx, a, after, limit)
}

// Patch uses the live moderator membership; Project admin is irrelevant here.
func (s *Service) Patch(ctx context.Context, a identity.Actor, id string, p Patch, tr policy.Trace) (Conversation, error) {
	if a.User.Banned {
		return Conversation{}, identity.ErrBanned
	}
	if !ValidID(id) {
		return Conversation{}, policy.ErrInvalid
	}
	if err := p.Validate(); err != nil {
		return Conversation{}, err
	}
	return s.Store.Patch(ctx, a, id, p, tr)
}
