// Package moderation owns Project content policy and administrative moderation data.
package moderation

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/google/uuid"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// ErrContentRejected intentionally omits the matched term from API/log output.
var ErrContentRejected = errors.New("CONTENT_REJECTED")

type Entry struct {
	ID        string    `json:"id"`
	Word      string    `json:"normalized_word"`
	Enabled   bool      `json:"enabled"`
	Version   int64     `json:"resource_version,string"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Create struct {
	Word string `json:"word"`
}
type Patch struct {
	Enabled         *bool `json:"enabled"`
	ExpectedVersion int64 `json:"expected_version,string"`
}

// Words applies NFC, Unicode case folding and whole letter/digit tokenization.
// Punctuation is a boundary, so matching is deterministic and not substring-based.
func Words(text string) []string {
	normalized := norm.NFC.String(cases.Fold().String(norm.NFC.String(text)))
	return strings.FieldsFunc(normalized, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func (c Create) Normalize() (string, error) {
	if !utf8.ValidString(c.Word) || len(c.Word) > 256 {
		return "", policy.ErrInvalid
	}
	words := Words(c.Word)
	if len(words) != 1 || words[0] != norm.NFC.String(cases.Fold().String(strings.TrimSpace(c.Word))) || len(words[0]) > 256 {
		return "", policy.ErrInvalid
	}
	return words[0], nil
}

func (p Patch) Validate() error {
	if p.Enabled == nil || p.ExpectedVersion < 1 {
		return policy.ErrInvalid
	}
	return nil
}

type Store interface {
	List(context.Context, identity.Actor, string, int) ([]Entry, error)
	Create(context.Context, identity.Actor, string, policy.Trace) (Entry, error)
	Update(context.Context, identity.Actor, string, Patch, policy.Trace) (Entry, error)
	Delete(context.Context, identity.Actor, string, policy.Trace) error
}

type Service struct{ Store Store }

func admin(a identity.Actor) error {
	if !a.Admin {
		return policy.ErrForbidden
	}
	if a.User.Banned {
		return identity.ErrBanned
	}
	return nil
}

func (s *Service) List(ctx context.Context, a identity.Actor, after string, limit int) ([]Entry, error) {
	// Global bans are read-only rather than invisible; only mutations call admin.
	if !a.Admin {
		return nil, policy.ErrForbidden
	}
	if after != "" && uuid.Validate(after) != nil || limit < 1 || limit > 101 {
		return nil, policy.ErrInvalid
	}
	return s.Store.List(ctx, a, after, limit)
}

func (s *Service) Create(ctx context.Context, a identity.Actor, command Create, trace policy.Trace) (Entry, error) {
	if err := admin(a); err != nil {
		return Entry{}, err
	}
	word, err := command.Normalize()
	if err != nil {
		return Entry{}, err
	}
	return s.Store.Create(ctx, a, word, trace)
}

func (s *Service) Update(ctx context.Context, a identity.Actor, id string, patch Patch, trace policy.Trace) (Entry, error) {
	if err := admin(a); err != nil {
		return Entry{}, err
	}
	if uuid.Validate(id) != nil || patch.Validate() != nil {
		return Entry{}, policy.ErrInvalid
	}
	return s.Store.Update(ctx, a, id, patch, trace)
}

func (s *Service) Delete(ctx context.Context, a identity.Actor, id string, trace policy.Trace) error {
	if err := admin(a); err != nil {
		return err
	}
	if uuid.Validate(id) != nil {
		return policy.ErrInvalid
	}
	return s.Store.Delete(ctx, a, id, trace)
}
