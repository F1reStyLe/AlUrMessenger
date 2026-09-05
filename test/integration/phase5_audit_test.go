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
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/F1reStyLe/AlUrMessenger/internal/provision"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testPhase5Audit is the cross-feature acceptance matrix. Individual suites
// probe deeper races and boundaries; this test proves every Phase 5 feature flag
// gates retries too and every historical mutation hydrates one final DTO.
func testPhase5Audit(t *testing.T, db *pgxpool.Pool, op *pgx.Conn) {
	ctx := t.Context()
	project := uuid.NewString()
	if err := provision.Create(ctx, op, provision.Project{ID: project, Name: "Phase 5 audit", Issuer: "https://issuer.test", Audience: "chat"}); err != nil {
		t.Fatal(err)
	}
	actor := func(name string) identity.Actor {
		a := identity.Actor{ProjectID: project, User: identity.User{ID: uuid.NewString(), Kind: "human", Role: "user"}}
		if _, err := op.Exec(ctx, "INSERT INTO chat.users(id,project_id,external_user_id,display_name) VALUES($1::uuid,$2,$1::text,$3)", a.User.ID, project, name); err != nil {
			t.Fatal(err)
		}
		return a
	}
	a, b := actor("Audit Alice"), actor("Audit Bob")
	convs := &conversation.Service{Store: &conversationrepo.Store{DB: db}}
	trace := policy.Trace{RequestID: uuid.NewString()}
	create := func() string {
		c, _, err := convs.Create(ctx, a, conversation.Create{Type: "GROUP", MemberIDs: []string{b.User.ID}}, trace)
		if err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	sourceConversation, target := create(), create()
	cfg, err := cryptography.Generate()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := cryptography.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	store := &messagerepo.Store{DB: db, Crypto: keys}
	svc := &message.Service{Store: store}
	recovery := &message.RecoveryService{Store: store}
	send := func(who identity.Actor, conv, text string) message.Sent {
		got, e := svc.Send(ctx, who, conv, message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: text}})
		if e != nil {
			t.Fatal(e)
		}
		return got
	}
	setFlag := func(flag string, enabled bool) {
		if _, e := op.Exec(ctx, "UPDATE chat.project_settings SET flags=jsonb_set(flags,ARRAY[$2],to_jsonb($3::boolean)) WHERE project_id=$1", project, flag, enabled); e != nil {
			t.Fatal(e)
		}
	}
	assertDisabled := func(flag string, call func() error) {
		t.Helper()
		setFlag(flag, false)
		if e := call(); !errors.Is(e, policy.ErrFeatureDisabled) {
			t.Fatalf("%s did not gate operation/retry: %v", flag, e)
		}
		setFlag(flag, true)
	}

	source := send(b, sourceConversation, "sourceword")
	parent := send(a, target, "parentword")
	replyCommand := message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: "replyword"}, ReplyTo: &parent.Message.ID}
	reply, err := svc.Send(ctx, b, target, replyCommand)
	if err != nil {
		t.Fatal(err)
	}
	assertDisabled("allow_reply", func() error { _, e := svc.Send(ctx, b, target, replyCommand); return e })

	forwardCommand := message.Send{ClientID: uuid.NewString(), ForwardFrom: &source.Message.ID}
	forwarded, err := svc.Send(ctx, a, target, forwardCommand)
	if err != nil {
		t.Fatal(err)
	}
	assertDisabled("allow_forward", func() error { _, e := svc.Send(ctx, a, target, forwardCommand); return e })

	forwarded.Message, err = svc.Edit(ctx, a, forwarded.Message.ID, target, message.Edit{Content: message.Content{Text: "forwardedited"}, ExpectedVersion: forwarded.Message.Version})
	if err != nil {
		t.Fatal(err)
	}
	assertDisabled("allow_edit", func() error {
		_, e := svc.Edit(ctx, a, forwarded.Message.ID, target, message.Edit{Content: message.Content{Text: "blockededit"}, ExpectedVersion: forwarded.Message.Version})
		return e
	})

	forwarded.Message, err = svc.React(ctx, b, forwarded.Message.ID, target, "👍", true)
	if err != nil {
		t.Fatal(err)
	}
	assertDisabled("allow_reactions", func() error { _, e := svc.React(ctx, b, forwarded.Message.ID, target, "👍", true); return e })
	forwarded.Message, err = svc.React(ctx, b, forwarded.Message.ID, target, "👍", false)
	if err != nil {
		t.Fatal(err)
	}

	forwarded.Message, err = svc.SetPin(ctx, a, target, forwarded.Message.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	assertDisabled("allow_pin", func() error { _, e := svc.SetPin(ctx, a, target, forwarded.Message.ID, true); return e })
	forwarded.Message, err = svc.SetPin(ctx, a, target, forwarded.Message.ID, false)
	if err != nil {
		t.Fatal(err)
	}

	deletable := send(a, target, "deleteflagword")
	assertDisabled("allow_delete", func() error { _, e := svc.Delete(ctx, a, deletable.Message.ID, target); return e })
	if _, err = svc.Delete(ctx, a, deletable.Message.ID, target); err != nil {
		t.Fatal(err)
	}
	assertDisabled("allow_delete", func() error { _, e := svc.Delete(ctx, a, deletable.Message.ID, target); return e })

	// A final delete must redact forward attribution in every current-state path,
	// while an independent reply remains live with only its safe parent reference.
	if _, err = svc.Delete(ctx, a, parent.Message.ID, target); err != nil {
		t.Fatal(err)
	}
	terminal, err := svc.Delete(ctx, a, forwarded.Message.ID, target)
	assertTombstone(t, terminal, err, "deleted")
	currentReply, err := svc.Get(ctx, b, reply.Message.ID)
	if err != nil || currentReply.Reply == nil || currentReply.Reply.MessageID != parent.Message.ID || currentReply.Content == nil {
		t.Fatal("reply current state", err)
	}
	history, err := svc.History(ctx, a, target, message.Query{Forward: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	foundTerminal := false
	for _, item := range history {
		if item.ID == terminal.ID {
			assertTombstone(t, item, nil, "deleted")
			foundTerminal = true
		}
	}
	if !foundTerminal {
		t.Fatal("terminal missing from history")
	}
	if hits, e := svc.Search(ctx, a, target, "forwardedited", message.Query{Limit: 100}); e != nil || len(hits) != 0 {
		t.Fatal("terminal remained searchable", e)
	}
	snapshot, err := recovery.Snapshot(ctx, a, target)
	if err != nil {
		t.Fatal(err)
	}
	foundTerminal = false
	for _, item := range snapshot.Messages {
		if item.ID == terminal.ID {
			assertTombstone(t, item, nil, "deleted")
			foundTerminal = true
		}
	}
	if !foundTerminal || len(snapshot.Pins) != 0 {
		t.Fatal("snapshot did not replace Phase 5 state")
	}
	replay, err := recovery.Replay(ctx, a, target, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, event := range replay.Events {
		if event.Payload["message_id"] != terminal.ID {
			continue
		}
		seen[event.Type] = true
		m, ok := event.Payload["message"].(message.Message)
		if !ok {
			t.Fatal("message event was not hydrated", event.Type)
		}
		assertTombstone(t, m, nil, "deleted")
	}
	for _, kind := range []string{"message.created", "message.updated", "reaction.created", "reaction.deleted", "message.pinned", "message.unpinned", "message.deleted"} {
		if !seen[kind] {
			t.Fatal("missing Phase 5 state event", kind)
		}
	}
	var persistedHydration bool
	if err = op.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM chat.conversation_events WHERE project_id=$1 AND conversation_id=$2 AND envelope->'payload' ? 'message')", project, target).Scan(&persistedHydration); err != nil || persistedHydration {
		t.Fatal("current state persisted in changefeed", err)
	}
}
