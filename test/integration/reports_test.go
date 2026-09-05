//go:build integration

package integration

import (
	"bytes"
	"errors"
	"testing"

	"github.com/F1reStyLe/AlUrMessenger/internal/conversation"
	conversationrepo "github.com/F1reStyLe/AlUrMessenger/internal/conversation/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/cryptography"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/message"
	messagerepo "github.com/F1reStyLe/AlUrMessenger/internal/message/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/moderation"
	moderationrepo "github.com/F1reStyLe/AlUrMessenger/internal/moderation/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/infrastructure"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/F1reStyLe/AlUrMessenger/internal/provision"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func testReports(t *testing.T, clients *infrastructure.Clients, op *pgx.Conn) {
	ctx := t.Context()
	project := uuid.NewString()
	if err := provision.Create(ctx, op, provision.Project{ID: project, Name: "Reports", Issuer: "https://issuer.test", Audience: "chat"}); err != nil {
		t.Fatal(err)
	}
	actor := func(name, kind, role string) identity.Actor {
		a := identity.Actor{ProjectID: project, Admin: role == "admin", User: identity.User{ID: uuid.NewString(), Kind: kind, Role: role}}
		if _, err := op.Exec(ctx, "INSERT INTO chat.users(id,project_id,external_user_id,display_name,kind,role) VALUES($1::uuid,$2,CASE WHEN $4='human' THEN $1::text ELSE NULL END,$3,$4,$5)", a.User.ID, project, name, kind, role); err != nil {
			t.Fatal(err)
		}
		return a
	}
	admin, reporter, target := actor("Admin", "human", "admin"), actor("Reporter", "human", "user"), actor("Target", "human", "user")
	outsider, bot, system := actor("Outsider", "human", "user"), actor("Bot", "bot", "user"), actor("System", "system", "user")
	trace := policy.Trace{RequestID: uuid.NewString(), IP: "127.0.0.1"}
	conversations := &conversation.Service{Store: &conversationrepo.Store{DB: clients.Postgres}}
	chat, _, err := conversations.Create(ctx, reporter, conversation.Create{Type: "GROUP", MemberIDs: []string{target.User.ID}}, trace)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := cryptography.Generate()
	keys, _ := cryptography.New(cfg)
	messages := &message.Service{Store: &messagerepo.Store{DB: clients.Postgres, Crypto: keys}}
	sent, err := messages.Send(ctx, target, chat.ID, message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: "reported message"}})
	if err != nil {
		t.Fatal(err)
	}
	reports := &moderation.ReportService{Store: &moderationrepo.Reports{DB: clients.Postgres, Crypto: keys}}
	messageReport, err := reports.CreateMessage(ctx, reporter, sent.Message.ID, moderation.ReportCreate{Reason: "abuse", Description: "private evidence"})
	if err != nil || messageReport.MessageID == nil || *messageReport.MessageID != sent.Message.ID || messageReport.Status != "OPEN" || messageReport.Version != 1 {
		t.Fatal(messageReport, err)
	}
	var ciphertext []byte
	if err = op.QueryRow(ctx, "SELECT encrypted_description FROM chat.reports WHERE project_id=$1 AND id=$2", project, messageReport.ID).Scan(&ciphertext); err != nil || bytes.Contains(ciphertext, []byte("private evidence")) {
		t.Fatal("report description persisted in plaintext", err)
	}
	userReport, err := reports.CreateUser(ctx, reporter, target.User.ID, moderation.ReportCreate{Reason: "spam", Description: "account evidence", ConversationID: &chat.ID})
	if err != nil || userReport.TargetUserID == nil || *userReport.TargetUserID != target.User.ID || userReport.ConversationID == nil {
		t.Fatal(userReport, err)
	}
	if _, err = reports.CreateUser(ctx, reporter, reporter.User.ID, moderation.ReportCreate{Reason: "spam", Description: "self"}); !errors.Is(err, policy.ErrInvalid) {
		t.Fatal("self-report accepted", err)
	}
	if _, err = reports.CreateUser(ctx, bot, target.User.ID, moderation.ReportCreate{Reason: "spam", Description: "bot"}); !errors.Is(err, policy.ErrInvalid) {
		t.Fatal("bot reporter accepted", err)
	}
	if _, err = reports.CreateUser(ctx, reporter, system.User.ID, moderation.ReportCreate{Reason: "spam", Description: "system"}); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("system target accepted", err)
	}
	if _, err = reports.CreateMessage(ctx, outsider, sent.Message.ID, moderation.ReportCreate{Reason: "abuse", Description: "not a member"}); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("non-member reported private message", err)
	}
	owned, err := reports.GetOwn(ctx, reporter, messageReport.ID)
	if err != nil || owned.Description != "private evidence" {
		t.Fatal(owned, err)
	}
	if _, err = reports.GetOwn(ctx, outsider, messageReport.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("foreign reporter read report", err)
	}
	if _, err = reports.GetOwn(ctx, admin, messageReport.ID); err != nil {
		t.Fatal("admin safe read failed", err)
	}
	items, err := reports.List(ctx, admin, "OPEN", "", 10)
	if err != nil || len(items) != 2 {
		t.Fatal(items, err)
	}
	if _, err = reports.Review(ctx, admin, messageReport.ID, moderation.Review{Status: "RESOLVED", ExpectedVersion: 1}, trace); !errors.Is(err, policy.ErrConflict) {
		t.Fatal("invalid transition accepted", err)
	}
	reviewing, err := reports.Review(ctx, admin, messageReport.ID, moderation.Review{Status: "REVIEWING", ExpectedVersion: 1}, trace)
	if err != nil || reviewing.Status != "REVIEWING" || reviewing.Version != 2 || reviewing.ReviewedBy != nil {
		t.Fatal(reviewing, err)
	}
	resolved, err := reports.Review(ctx, admin, messageReport.ID, moderation.Review{Status: "RESOLVED", ExpectedVersion: 2}, trace)
	if err != nil || resolved.Status != "RESOLVED" || resolved.Version != 3 || resolved.ReviewedBy == nil || *resolved.ReviewedBy != admin.User.ID || resolved.ReviewedAt == nil {
		t.Fatal(resolved, err)
	}
	if _, err = reports.Review(ctx, admin, messageReport.ID, moderation.Review{Status: "REJECTED", ExpectedVersion: 3}, trace); !errors.Is(err, policy.ErrConflict) {
		t.Fatal("terminal report changed", err)
	}
	if _, err = reports.Review(ctx, reporter, userReport.ID, moderation.Review{Status: "REVIEWING", ExpectedVersion: 1}, trace); !errors.Is(err, policy.ErrForbidden) {
		t.Fatal("non-admin reviewed report", err)
	}
	if _, err = clients.Postgres.Exec(ctx, "UPDATE chat.reports SET encrypted_description='x' WHERE project_id=$1 AND id=$2", project, userReport.ID); err == nil {
		t.Fatal("runtime changed immutable report evidence")
	}
	if _, err = clients.Postgres.Exec(ctx, "DELETE FROM chat.reports WHERE project_id=$1 AND id=$2", project, userReport.ID); err == nil {
		t.Fatal("runtime deleted report")
	}
	var auditCount, leaked int
	if err = op.QueryRow(ctx, "SELECT count(*) FROM chat.audit_logs WHERE project_id=$1 AND resource_type='report' AND resource_id=$2", project, messageReport.ID).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatal("review audit incomplete", auditCount, err)
	}
	if err = op.QueryRow(ctx, "SELECT count(*) FROM chat.audit_logs WHERE project_id=$1 AND metadata::text LIKE '%private evidence%'", project).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatal("report description leaked to audit", leaked, err)
	}
	if _, err = op.Exec(ctx, "UPDATE chat.reports SET encrypted_description=set_byte(encrypted_description,0,get_byte(encrypted_description,0)#1) WHERE project_id=$1 AND id=$2", project, userReport.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = reports.GetOwn(ctx, reporter, userReport.ID); !errors.Is(err, cryptography.ErrCrypto) {
		t.Fatal("tampered report decrypted", err)
	}
}
