// Package policy defines current Project settings, authorization and audit contracts.
package policy

import (
	"context"
	"errors"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"sort"
	"time"
)

// Stable errors are mapped to public HTTP codes without leaking database details.
var ErrForbidden = errors.New("FORBIDDEN")
var ErrConflict = errors.New("VERSION_CONFLICT")
var ErrInvalid = errors.New("INVALID_REQUEST")
var ErrFeatureDisabled = errors.New("FEATURE_DISABLED")

// FlagNames is a closed vocabulary; unknown flags must never silently enable a feature.
func FlagNames() []string {
	return []string{"allow_images", "allow_reactions", "allow_edit", "allow_delete", "allow_forward", "allow_reply", "allow_pin", "typing_enabled", "presence_enabled", "read_receipts", "allow_bots", "allow_webhooks"}
}

// Settings is the trusted database snapshot. Versions are strings for JS precision.
type Settings struct {
	ProjectID     string          `json:"project_id"`
	Name          string          `json:"name"`
	Flags         map[string]bool `json:"flags"`
	MaxUploadSize int64           `json:"max_upload_size"`
	RetentionDays int             `json:"message_retention_days"`
	Version       int64           `json:"settings_version,string"`
}

// RequireFeature is shared by future write use cases; invoke on the snapshot read
// under that write transaction's Project shared lock, not a stale UI/cache copy.
func (s Settings) RequireFeature(name string) error {
	if !s.Flags[name] {
		return ErrFeatureDisabled
	}
	return nil
}

// Patch can change only bounded settings, never issuer, role or Project identity.
type Patch struct {
	ExpectedVersion int64            `json:"expected_version,string"`
	Flags           map[string]*bool `json:"flags"`
	MaxUploadSize   *int64           `json:"max_upload_size"`
	RetentionDays   *int             `json:"message_retention_days"`
}

// Validate checks partial maps explicitly, including JSON null flag values.
func (p Patch) Validate() error {
	if p.ExpectedVersion < 1 || (len(p.Flags) == 0 && p.MaxUploadSize == nil && p.RetentionDays == nil) {
		return ErrInvalid
	}
	if p.MaxUploadSize != nil && (*p.MaxUploadSize < 1 || *p.MaxUploadSize > 50*1024*1024) {
		return ErrInvalid
	}
	if p.RetentionDays != nil && (*p.RetentionDays < 1 || *p.RetentionDays > 3650) {
		return ErrInvalid
	}
	known := map[string]bool{}
	for _, k := range FlagNames() {
		known[k] = true
	}
	for k, v := range p.Flags {
		if !known[k] || v == nil {
			return ErrInvalid
		}
	}
	return nil
}

// Fields supplies safe audit metadata; no values, secrets, token or content are logged.
func (p Patch) Fields() []string {
	fields := []string{}
	for k := range p.Flags {
		fields = append(fields, k)
	}
	if p.MaxUploadSize != nil {
		fields = append(fields, "max_upload_size")
	}
	if p.RetentionDays != nil {
		fields = append(fields, "message_retention_days")
	}
	sort.Strings(fields)
	return fields
}

// Trace contains middleware-validated request ID and direct peer IP, not forwarded headers.
type Trace struct{ RequestID, IP string }

// Audit is read-only via API and append-only with runtime SQL credentials.
type Audit struct {
	ID           string         `json:"id"`
	ActorID      string         `json:"actor_id"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID   string         `json:"resource_id"`
	Metadata     map[string]any `json:"metadata"`
	RequestID    string         `json:"request_id"`
	CreatedAt    time.Time      `json:"created_at"`
}

// Store keeps transaction and tenancy requirements explicit at the domain boundary.
type Store interface {
	Get(context.Context, string) (Settings, error)
	Update(context.Context, identity.Actor, Patch, Trace) (Settings, error)
	Audit(context.Context, string, string, int) ([]Audit, error)
}

// Service checks global role before storage access; repository rechecks inside mutations.
type Service struct{ Store Store }

func (s *Service) Get(ctx context.Context, a identity.Actor) (Settings, error) {
	if !a.Admin {
		return Settings{}, ErrForbidden
	}
	return s.Store.Get(ctx, a.ProjectID)
}

// Update has no admin bypass for banned actors. Database repeats the ban check under lock.
func (s *Service) Update(ctx context.Context, a identity.Actor, p Patch, tr Trace) (Settings, error) {
	if !a.Admin {
		return Settings{}, ErrForbidden
	}
	if a.User.Banned {
		return Settings{}, identity.ErrBanned
	}
	if err := p.Validate(); err != nil {
		return Settings{}, err
	}
	return s.Store.Update(ctx, a, p, tr)
}

// Audit only exposes the actor's Project, irrespective of any client-supplied IDs.
func (s *Service) Audit(ctx context.Context, a identity.Actor, after string, limit int) ([]Audit, error) {
	if !a.Admin {
		return nil, ErrForbidden
	}
	return s.Store.Audit(ctx, a.ProjectID, after, limit)
}
