//go:build integration

package integration

import (
	"errors"
	"sync"
	"testing"

	"github.com/F1reStyLe/AlUrMessenger/internal/attachment"
	attachmentrepo "github.com/F1reStyLe/AlUrMessenger/internal/attachment/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/conversation"
	conversationrepo "github.com/F1reStyLe/AlUrMessenger/internal/conversation/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/cryptography"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	identityrepo "github.com/F1reStyLe/AlUrMessenger/internal/identity/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/message"
	messagerepo "github.com/F1reStyLe/AlUrMessenger/internal/message/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/moderation"
	moderationrepo "github.com/F1reStyLe/AlUrMessenger/internal/moderation/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/infrastructure"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	policyrepo "github.com/F1reStyLe/AlUrMessenger/internal/policy/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/provision"
	"github.com/F1reStyLe/AlUrMessenger/internal/realtime"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// testBans exercises every implemented domain write family with a stale Actor.
// Repository checks, not a cached HTTP/WS identity bit, must enforce the ban.
func testBans(t *testing.T, clients *infrastructure.Clients, op *pgx.Conn) {
	ctx := t.Context()
	project := uuid.NewString()
	if err := provision.Create(ctx, op, provision.Project{ID: project, Name: "Bans", Issuer: "https://issuer.test", Audience: "chat"}); err != nil {
		t.Fatal(err)
	}
	actor := func(name, kind, role string) identity.Actor {
		a := identity.Actor{ProjectID: project, Admin: role == "admin", User: identity.User{ID: uuid.NewString(), Kind: kind, Role: role}}
		if _, err := op.Exec(ctx, "INSERT INTO chat.users(id,project_id,external_user_id,display_name,kind,role) VALUES($1::uuid,$2,CASE WHEN $4='human' THEN $1::text ELSE NULL END,$3,$4,$5)", a.User.ID, project, name, kind, role); err != nil {
			t.Fatal(err)
		}
		return a
	}
	admin, secondAdmin := actor("Admin", "human", "admin"), actor("Second Admin", "human", "admin")
	user, peer, system := actor("User", "human", "user"), actor("Peer", "human", "user"), actor("System", "system", "user")
	trace := policy.Trace{RequestID: uuid.NewString(), IP: "127.0.0.1"}
	convStore := &conversationrepo.Store{DB: clients.Postgres}
	conversations := &conversation.Service{Store: convStore}
	memberships := &conversation.MembershipService{Store: convStore}
	chat, _, err := conversations.Create(ctx, user, conversation.Create{Type: "GROUP", MemberIDs: []string{peer.User.ID}}, trace)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := cryptography.Generate()
	keys, _ := cryptography.New(cfg)
	messageStore := &messagerepo.Store{DB: clients.Postgres, Crypto: keys}
	messages := &message.Service{Store: messageStore}
	recovery := &message.RecoveryService{Store: messageStore}
	sent, err := messages.Send(ctx, user, chat.ID, message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: "before ban"}})
	if err != nil {
		t.Fatal(err)
	}
	reports := &moderation.ReportService{Store: &moderationrepo.Reports{DB: clients.Postgres, Crypto: keys}}
	report, err := reports.CreateUser(ctx, user, peer.User.ID, moderation.ReportCreate{Reason: "spam", Description: "before ban"})
	if err != nil {
		t.Fatal(err)
	}
	bans := &moderation.BanService{Store: &moderationrepo.Bans{DB: clients.Postgres}}
	state, err := bans.Ban(ctx, admin, user.User.ID, moderation.BanCommand{Reason: "policy violation", ExpectedVersion: 1}, trace)
	if err != nil || !state.Banned || state.Version != 2 || state.BannedAt == nil || state.Reason != "policy violation" {
		t.Fatal(state, err)
	}
	// Read-only means history/profile resources remain visible; only commands mutate.
	if _, err = conversations.Get(ctx, user, chat.ID); err != nil {
		t.Fatal("banned conversation read", err)
	}
	if _, err = messages.Get(ctx, user, sent.Message.ID); err != nil {
		t.Fatal("banned message read", err)
	}
	if _, err = messages.History(ctx, user, chat.ID, message.Query{Limit: 10}); err != nil {
		t.Fatal("banned history read", err)
	}
	if _, err = reports.GetOwn(ctx, user, report.ID); err != nil {
		t.Fatal("banned report read", err)
	}
	denied := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, identity.ErrBanned) {
			t.Fatal(name, "was not blocked", err)
		}
	}
	name := "changed"
	_, err = (&identityrepo.Store{DB: clients.Postgres}).PatchUser(ctx, user, identity.ProfilePatch{DisplayName: &name})
	denied("profile", err)
	_, _, err = conversations.Create(ctx, user, conversation.Create{Type: "GROUP"}, trace)
	denied("conversation create", err)
	title := "changed"
	_, err = conversations.Patch(ctx, user, chat.ID, conversation.Patch{Title: &title, ExpectedVersion: chat.Version}, trace)
	denied("conversation patch", err)
	_, err = memberships.Add(ctx, user, chat.ID, conversation.AddMembers{UserIDs: []string{secondAdmin.User.ID}}, trace)
	denied("member add", err)
	muted := true
	_, err = memberships.Patch(ctx, user, chat.ID, user.User.ID, conversation.MemberPatch{Muted: &muted, ExpectedVersion: chat.Membership.Version}, trace)
	denied("member patch", err)
	err = memberships.Remove(ctx, user, chat.ID, user.User.ID, trace)
	denied("member leave", err)
	_, err = messages.Send(ctx, user, chat.ID, message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: "blocked"}})
	denied("send", err)
	_, err = messages.Edit(ctx, user, sent.Message.ID, chat.ID, message.Edit{Content: message.Content{Text: "blocked"}, ExpectedVersion: sent.Message.Version})
	denied("edit", err)
	_, err = messages.Delete(ctx, user, sent.Message.ID, chat.ID)
	denied("delete", err)
	_, err = messages.React(ctx, user, sent.Message.ID, chat.ID, "👍", true)
	denied("reaction", err)
	_, err = messages.SetPin(ctx, user, chat.ID, sent.Message.ID, true)
	denied("pin", err)
	_, err = recovery.Receipt(ctx, user, chat.ID, "read", sent.Message.Sequence)
	denied("read receipt", err)
	denied("typing", (&realtime.Presence{DB: clients.Postgres, Redis: clients.Redis}).Typing(ctx, user, chat.ID, uuid.NewString(), true))
	_, err = attachment.New(&attachmentrepo.Store{DB: clients.Postgres}, clients.Storage, config.Upload{MaxBytes: 50 << 20}).Limit(ctx, user)
	denied("attachment upload", err)
	_, err = reports.CreateUser(ctx, user, secondAdmin.User.ID, moderation.ReportCreate{Reason: "spam", Description: "blocked"})
	denied("report", err)
	// Banned admins retain moderation/settings/audit reads, but every admin write fails.
	adminBan, err := bans.Ban(ctx, admin, secondAdmin.User.ID, moderation.BanCommand{Reason: "admin violation", ExpectedVersion: 1}, trace)
	if err != nil {
		t.Fatal(err)
	}
	blacklist := &moderation.Service{Store: &moderationrepo.Store{DB: clients.Postgres}}
	if _, err = blacklist.List(ctx, secondAdmin, "", 10); err != nil {
		t.Fatal("banned admin blacklist read", err)
	}
	if _, err = reports.List(ctx, secondAdmin, "", "", 10); err != nil {
		t.Fatal("banned admin report read", err)
	}
	settings := &policy.Service{Store: &policyrepo.Store{DB: clients.Postgres}}
	current, err := settings.Get(ctx, secondAdmin)
	if err != nil {
		t.Fatal("banned admin settings read", err)
	}
	if _, err = settings.Audit(ctx, secondAdmin, "", 10); err != nil {
		t.Fatal("banned admin audit read", err)
	}
	_, err = blacklist.Create(ctx, secondAdmin, moderation.Create{Word: "blocked"}, trace)
	denied("blacklist admin write", err)
	allow := false
	_, err = settings.Update(ctx, secondAdmin, policy.Patch{ExpectedVersion: current.Version, Flags: map[string]*bool{"allow_edit": &allow}}, trace)
	denied("settings admin write", err)
	_, err = reports.Review(ctx, secondAdmin, report.ID, moderation.Review{Status: "REVIEWING", ExpectedVersion: 1}, trace)
	denied("report review", err)
	if _, err = bans.Unban(ctx, admin, secondAdmin.User.ID, moderation.UnbanCommand{ExpectedVersion: adminBan.Version}, trace); err != nil {
		t.Fatal(err)
	}
	state, err = bans.Unban(ctx, admin, user.User.ID, moderation.UnbanCommand{ExpectedVersion: state.Version}, trace)
	if err != nil || state.Banned || state.Version != 3 || state.BannedAt != nil || state.Reason != "" {
		t.Fatal(state, err)
	}
	if _, err = messages.Send(ctx, user, chat.ID, message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: "after unban"}}); err != nil {
		t.Fatal("unban did not restore write", err)
	}
	if _, err = bans.Ban(ctx, admin, system.User.ID, moderation.BanCommand{Reason: "system", ExpectedVersion: 1}, trace); !errors.Is(err, policy.ErrForbidden) {
		t.Fatal("system ban accepted", err)
	}
	if _, err = bans.Ban(ctx, admin, admin.User.ID, moderation.BanCommand{Reason: "self", ExpectedVersion: 1}, trace); !errors.Is(err, policy.ErrInvalid) {
		t.Fatal("self-ban accepted", err)
	}
	if _, err = bans.Ban(ctx, admin, user.User.ID, moderation.BanCommand{Reason: "stale", ExpectedVersion: 1}, trace); !errors.Is(err, policy.ErrConflict) {
		t.Fatal("stale ban accepted", err)
	}
	// A racing send either commits before the ban or observes it; it can never commit after.
	raceUser := actor("Race", "human", "user")
	raceChat, _, err := conversations.Create(ctx, raceUser, conversation.Create{Type: "GROUP"}, trace)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, e := messages.Send(ctx, raceUser, raceChat.ID, message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: "race"}})
		results <- e
	}()
	go func() {
		defer wg.Done()
		<-start
		_, e := bans.Ban(ctx, admin, raceUser.User.ID, moderation.BanCommand{Reason: "race", ExpectedVersion: 1}, trace)
		results <- e
	}()
	close(start)
	wg.Wait()
	close(results)
	for result := range results {
		if result != nil && !errors.Is(result, identity.ErrBanned) {
			t.Fatal("race failed", result)
		}
	}
	if _, err = messages.Send(ctx, raceUser, raceChat.ID, message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: "after race"}}); !errors.Is(err, identity.ErrBanned) {
		t.Fatal("post-ban send committed", err)
	}
	var audits, reasonLeaks int
	if err = op.QueryRow(ctx, "SELECT count(*) FROM chat.audit_logs WHERE project_id=$1 AND action IN ('user.banned','user.unbanned')", project).Scan(&audits); err != nil || audits != 5 {
		t.Fatal("ban audit count", audits, err)
	}
	if err = op.QueryRow(ctx, "SELECT count(*) FROM chat.audit_logs WHERE project_id=$1 AND metadata::text LIKE '%violation%'", project).Scan(&reasonLeaks); err != nil || reasonLeaks != 0 {
		t.Fatal("ban reason leaked to audit", reasonLeaks, err)
	}
}
