//go:build integration

package integration

import (
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
	policyrepo "github.com/F1reStyLe/AlUrMessenger/internal/policy/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/provision"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// testBlacklist proves that one transactional gate covers human, bot, internal
// SYSTEM, IMAGE caption, edit and forward command paths without persisting body.
func testBlacklist(t *testing.T, clients *infrastructure.Clients, op *pgx.Conn) {
	ctx := t.Context()
	project := uuid.NewString()
	if err := provision.Create(ctx, op, provision.Project{ID: project, Name: "Blacklist", Issuer: "https://issuer.test", Audience: "chat"}); err != nil {
		t.Fatal(err)
	}
	actor := func(name, kind, role string) identity.Actor {
		a := identity.Actor{ProjectID: project, Admin: role == "admin", User: identity.User{ID: uuid.NewString(), Kind: kind, Role: role}}
		if _, err := op.Exec(ctx, "INSERT INTO chat.users(id,project_id,external_user_id,display_name,kind,role) VALUES($1::uuid,$2,$1::text,$3,$4,$5)", a.User.ID, project, name, kind, role); err != nil {
			t.Fatal(err)
		}
		return a
	}
	admin, human, bot, system := actor("Admin", "human", "admin"), actor("Human", "human", "user"), actor("Bot", "bot", "user"), actor("System", "system", "user")
	trace := policy.Trace{RequestID: uuid.NewString(), IP: "127.0.0.1"}
	blacklist := &moderation.Service{Store: &moderationrepo.Store{DB: clients.Postgres}}
	entry, err := blacklist.Create(ctx, admin, moderation.Create{Word: "  ЗаПрЕт  "}, trace)
	if err != nil || entry.Word != "запрет" || !entry.Enabled || entry.Version != 1 {
		t.Fatal(entry, err)
	}
	if _, err = blacklist.Create(ctx, admin, moderation.Create{Word: "ЗАПРЕТ"}, trace); !errors.Is(err, policy.ErrConflict) {
		t.Fatal("normalized duplicate accepted", err)
	}
	items, err := blacklist.List(ctx, admin, "", 10)
	if err != nil || len(items) != 1 || items[0].ID != entry.ID {
		t.Fatal(items, err)
	}
	settings := &policy.Service{Store: &policyrepo.Store{DB: clients.Postgres}}
	current, err := settings.Get(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	enabled, reject, allowBots := true, "reject", true
	current, err = settings.Update(ctx, admin, policy.Patch{ExpectedVersion: current.Version, BlacklistEnabled: &enabled, BlacklistPolicy: &reject, Flags: map[string]*bool{"allow_bots": &allowBots}}, trace)
	if err != nil || !current.BlacklistEnabled || current.BlacklistPolicy != "reject" {
		t.Fatal(current, err)
	}
	conversations := &conversation.Service{Store: &conversationrepo.Store{DB: clients.Postgres}}
	chat, _, err := conversations.Create(ctx, human, conversation.Create{Type: "GROUP", MemberIDs: []string{bot.User.ID}}, trace)
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := conversations.Create(ctx, human, conversation.Create{Type: "GROUP"}, trace)
	if err != nil {
		t.Fatal(err)
	}
	keyConfig, _ := cryptography.Generate()
	keys, _ := cryptography.New(keyConfig)
	messages := &message.Service{Store: &messagerepo.Store{DB: clients.Postgres, Crypto: keys}}
	rejected := func(actor identity.Actor, conversationID string, command message.Send) {
		t.Helper()
		if _, err := messages.Send(ctx, actor, conversationID, command); !errors.Is(err, moderation.ErrContentRejected) {
			t.Fatal("blacklist bypass", command.Type, err)
		}
	}
	rejected(human, chat.ID, message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: "это ЗАПРЕТ!"}})
	rejected(bot, chat.ID, message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: "запрет"}})
	rejected(human, chat.ID, message.Send{ClientID: uuid.NewString(), Type: "IMAGE", Content: message.Content{Caption: "запрет"}, AttachmentIDs: []string{uuid.NewString()}})
	if _, err := messages.SendSystem(ctx, system, chat.ID, message.Send{ClientID: uuid.NewString(), Type: "SYSTEM", Content: message.Content{Text: "запрет"}}); !errors.Is(err, moderation.ErrContentRejected) {
		t.Fatal("internal SYSTEM bypass", err)
	}
	var sequence, messagesCount, outboxCount int
	if err = op.QueryRow(ctx, "SELECT message_sequence FROM chat.conversations WHERE project_id=$1 AND id=$2", project, chat.ID).Scan(&sequence); err != nil || sequence != 0 {
		t.Fatal("rejected content allocated sequence", sequence, err)
	}
	if err = op.QueryRow(ctx, "SELECT count(*) FROM chat.messages WHERE project_id=$1 AND conversation_id=$2", project, chat.ID).Scan(&messagesCount); err != nil || messagesCount != 0 {
		t.Fatal("rejected content persisted", messagesCount, err)
	}
	if err = op.QueryRow(ctx, `SELECT count(*) FROM chat.outbox_events o JOIN chat.conversation_events e
 ON e.project_id=o.project_id AND e.conversation_id=o.conversation_id AND e.id=o.event_id
 WHERE o.project_id=$1 AND o.conversation_id=$2 AND e.envelope->>'event_type'='message.created'`, project, chat.ID).Scan(&outboxCount); err != nil || outboxCount != 0 {
		t.Fatal("rejected content emitted event", outboxCount, err)
	}
	allowed, err := messages.Send(ctx, human, chat.ID, message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: "разрешено"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = messages.Edit(ctx, human, allowed.Message.ID, chat.ID, message.Edit{Content: message.Content{Text: "запрет"}, ExpectedVersion: 1}); !errors.Is(err, moderation.ErrContentRejected) {
		t.Fatal("edit bypassed blacklist", err)
	}
	// Create a forbidden source while policy is disabled, then ensure re-enabled
	// target forwarding validates the copied snapshot rather than trusting source.
	disabled := false
	current, err = settings.Update(ctx, admin, policy.Patch{ExpectedVersion: current.Version, BlacklistEnabled: &disabled}, trace)
	if err != nil {
		t.Fatal(err)
	}
	source, err := messages.Send(ctx, human, chat.ID, message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: "запрет"}})
	if err != nil {
		t.Fatal(err)
	}
	current, err = settings.Update(ctx, admin, policy.Patch{ExpectedVersion: current.Version, BlacklistEnabled: &enabled}, trace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = messages.Send(ctx, human, target.ID, message.Send{ClientID: uuid.NewString(), ForwardFrom: &source.Message.ID}); !errors.Is(err, moderation.ErrContentRejected) {
		t.Fatal("forward bypassed blacklist", err)
	}
	falseValue := false
	entry, err = blacklist.Update(ctx, admin, entry.ID, moderation.Patch{Enabled: &falseValue, ExpectedVersion: entry.Version}, trace)
	if err != nil || entry.Enabled || entry.Version != 2 {
		t.Fatal(entry, err)
	}
	if _, err = blacklist.Update(ctx, admin, entry.ID, moderation.Patch{Enabled: &enabled, ExpectedVersion: 1}, trace); !errors.Is(err, policy.ErrConflict) {
		t.Fatal("stale blacklist update accepted", err)
	}
	if _, err = messages.Send(ctx, human, target.ID, message.Send{ClientID: uuid.NewString(), ForwardFrom: &source.Message.ID}); err != nil {
		t.Fatal("disabled entry still rejected content", err)
	}
	if err = blacklist.Delete(ctx, admin, entry.ID, trace); err != nil {
		t.Fatal(err)
	}
	if err = blacklist.Delete(ctx, admin, entry.ID, trace); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("repeat delete did not hide absence", err)
	}
	var auditCount int
	if err = op.QueryRow(ctx, "SELECT count(*) FROM chat.audit_logs WHERE project_id=$1 AND action IN ('blacklist.created','blacklist.updated','blacklist.deleted')", project).Scan(&auditCount); err != nil || auditCount != 3 {
		t.Fatal("blacklist audit incomplete", auditCount, err)
	}
}
