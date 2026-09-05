package moderation

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/F1reStyLe/AlUrMessenger/internal/cryptography"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/google/uuid"
)

type ReportCreate struct {
	Reason         string  `json:"reason"`
	Description    string  `json:"description"`
	ConversationID *string `json:"conversation_id,omitempty"`
}

func (p ReportCreate) Validate(user bool) error {
	validReason := p.Reason == "spam" || p.Reason == "harassment" || p.Reason == "abuse" || p.Reason == "impersonation" || p.Reason == "other"
	if !validReason || !utf8.ValidString(p.Description) || utf8.RuneCountInString(p.Description) > 4000 || strings.TrimSpace(p.Description) == "" {
		return policy.ErrInvalid
	}
	if p.ConversationID != nil && uuid.Validate(*p.ConversationID) != nil || !user && p.ConversationID != nil {
		return policy.ErrInvalid
	}
	return nil
}

type Report struct {
	ID             string    `json:"id"`
	ReporterID     string    `json:"reporter_id"`
	TargetUserID   *string   `json:"target_user_id,omitempty"`
	MessageID      *string   `json:"message_id,omitempty"`
	ConversationID *string   `json:"conversation_id,omitempty"`
	Reason         string    `json:"reason"`
	Description    string    `json:"description"`
	Status         string    `json:"status"`
	Version        int64     `json:"resource_version,string"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type ReviewedReport struct {
	Report
	ReviewedBy *string    `json:"reviewed_by,omitempty"`
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`
}

type Review struct {
	Status          string `json:"status"`
	ExpectedVersion int64  `json:"expected_version,string"`
}

func (p Review) Validate() error {
	if p.ExpectedVersion < 1 || p.Status != "REVIEWING" && p.Status != "RESOLVED" && p.Status != "REJECTED" {
		return policy.ErrInvalid
	}
	return nil
}

type ReportCrypto interface {
	SealReport(string, string, []byte) (cryptography.Envelope, error)
	OpenReport(string, string, cryptography.Envelope) ([]byte, error)
}

type ReportStore interface {
	CreateMessage(context.Context, identity.Actor, string, ReportCreate) (Report, error)
	CreateUser(context.Context, identity.Actor, string, ReportCreate) (Report, error)
	GetOwn(context.Context, identity.Actor, string) (Report, error)
	List(context.Context, identity.Actor, string, string, int) ([]ReviewedReport, error)
	GetAdmin(context.Context, identity.Actor, string) (ReviewedReport, error)
	Review(context.Context, identity.Actor, string, Review, policy.Trace) (ReviewedReport, error)
}

type ReportService struct{ Store ReportStore }

func (s *ReportService) CreateMessage(ctx context.Context, actor identity.Actor, id string, command ReportCreate) (Report, error) {
	if actor.User.Kind != "human" || uuid.Validate(id) != nil || command.Validate(false) != nil {
		return Report{}, policy.ErrInvalid
	}
	return s.Store.CreateMessage(ctx, actor, id, command)
}

func (s *ReportService) CreateUser(ctx context.Context, actor identity.Actor, id string, command ReportCreate) (Report, error) {
	if actor.User.Kind != "human" || uuid.Validate(id) != nil || command.Validate(true) != nil {
		return Report{}, policy.ErrInvalid
	}
	return s.Store.CreateUser(ctx, actor, id, command)
}

func (s *ReportService) GetOwn(ctx context.Context, actor identity.Actor, id string) (Report, error) {
	if uuid.Validate(id) != nil {
		return Report{}, policy.ErrInvalid
	}
	return s.Store.GetOwn(ctx, actor, id)
}

func (s *ReportService) List(ctx context.Context, actor identity.Actor, status, after string, limit int) ([]ReviewedReport, error) {
	if !actor.Admin {
		return nil, policy.ErrForbidden
	}
	if status != "" && status != "OPEN" && status != "REVIEWING" && status != "RESOLVED" && status != "REJECTED" || after != "" && uuid.Validate(after) != nil || limit < 1 || limit > 101 {
		return nil, policy.ErrInvalid
	}
	return s.Store.List(ctx, actor, status, after, limit)
}

func (s *ReportService) GetAdmin(ctx context.Context, actor identity.Actor, id string) (ReviewedReport, error) {
	// A Project ban makes an admin read-only; it does not hide the moderation
	// queue. Every mutation still passes admin(), including the DB recheck.
	if !actor.Admin {
		return ReviewedReport{}, policy.ErrForbidden
	}
	if uuid.Validate(id) != nil {
		return ReviewedReport{}, policy.ErrInvalid
	}
	return s.Store.GetAdmin(ctx, actor, id)
}

func (s *ReportService) Review(ctx context.Context, actor identity.Actor, id string, command Review, trace policy.Trace) (ReviewedReport, error) {
	if err := admin(actor); err != nil {
		return ReviewedReport{}, err
	}
	if uuid.Validate(id) != nil || command.Validate() != nil {
		return ReviewedReport{}, policy.ErrInvalid
	}
	return s.Store.Review(ctx, actor, id, command, trace)
}
