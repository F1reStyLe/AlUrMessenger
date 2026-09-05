//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"sync"
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

// testRelations uses real runtime grants and independent connections to prove
// association uniqueness, authorization and atomic versions/events under races.
func testRelations(t *testing.T, db *pgxpool.Pool, op *pgx.Conn) {
	ctx := t.Context()
	project := uuid.NewString()
	if err := provision.Create(ctx, op, provision.Project{ID: project, Name: "Relations", Issuer: "https://issuer.test", Audience: "chat"}); err != nil {
		t.Fatal(err)
	}
	actor := func() identity.Actor {
		a := identity.Actor{User: identity.User{ID: uuid.NewString(), Kind: "human", Role: "user"}}
		a.ProjectID = project
		if _, err := op.Exec(ctx, "INSERT INTO chat.users(id,project_id,external_user_id,display_name) VALUES($1::uuid,$2,$1::text,'Fixture')", a.User.ID, project); err != nil {
			t.Fatal(err)
		}
		return a
	}
	a, b, outsider := actor(), actor(), actor()
	convs := &conversation.Service{Store: &conversationrepo.Store{DB: db}}
	trace := policy.Trace{RequestID: uuid.NewString()}
	create := func(kind string) string {
		c, _, err := convs.Create(ctx, a, conversation.Create{Type: kind, MemberIDs: []string{b.User.ID}}, trace)
		if err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	c, other, direct := create("CHANNEL"), create("GROUP"), create("DIRECT")
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
	send := func(conv string) message.Message {
		m, e := svc.Send(ctx, a, conv, message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: "relationsbody"}})
		if e != nil {
			t.Fatal(e)
		}
		return m.Message
	}
	m := send(c)
	// A CHANNEL reader may react, while pinning still requires moderator role.
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Go(func() { _, e := svc.React(ctx, b, m.ID, c, "❤", true); results <- e })
	}
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal(e)
		}
	}
	current, err := svc.Get(ctx, a, m.ID)
	if err != nil || current.Version != 2 || len(current.Reactions) != 1 || current.Reactions[0].Emoji != "❤️" || current.Reactions[0].Count != 1 {
		t.Fatal("duplicate reaction", current, err)
	}
	current, err = svc.React(ctx, a, m.ID, "", "❤️", true)
	if err != nil || current.Version != 3 || current.Reactions[0].Count != 2 {
		t.Fatal("multiple actors", err)
	}
	current, err = svc.React(ctx, b, m.ID, c, "❤️", false)
	if err != nil || current.Version != 4 || current.Reactions[0].Count != 1 {
		t.Fatal("remove own", err)
	}
	current, err = svc.React(ctx, b, m.ID, c, "❤", false)
	if err != nil || current.Version != 4 || current.Reactions[0].Count != 1 {
		t.Fatal("removed another actor", err)
	}
	if _, err = svc.SetPin(ctx, b, c, m.ID, true); !errors.Is(err, policy.ErrForbidden) {
		t.Fatal("reader pinned", err)
	}
	// An admin label alone cannot grant moderator rights in this conversation.
	b.User.Role = "admin"
	if _, err = svc.SetPin(ctx, b, c, m.ID, true); !errors.Is(err, policy.ErrForbidden) {
		t.Fatal("admin bypass", err)
	}
	if _, err = svc.React(ctx, outsider, m.ID, "", "👍", true); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("outsider", err)
	}
	foreign := a
	foreign.ProjectID = uuid.NewString()
	if _, err = svc.React(ctx, foreign, m.ID, "", "👍", true); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("foreign project", err)
	}
	if _, err = svc.SetPin(ctx, a, other, m.ID, true); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("cross-conversation pin", err)
	}
	if _, err = svc.React(ctx, a, m.ID, other, "👍", true); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("WS scope", err)
	}
	d := send(direct)
	if _, err = svc.SetPin(ctx, a, direct, d.ID, true); !errors.Is(err, policy.ErrForbidden) {
		t.Fatal("DIRECT pin", err)
	}
	// DB FK is a second isolation boundary, including for a valid foreign message.
	if _, err = db.Exec(ctx, "INSERT INTO chat.pinned_messages(project_id,conversation_id,message_id,pinned_by) VALUES($1,$2,$3,$4)", project, other, m.ID, a.User.ID); err == nil {
		t.Fatal("pin FK bypass")
	}
	if _, err = db.Exec(ctx, "INSERT INTO chat.message_reactions(project_id,conversation_id,message_id,user_id,reaction) VALUES($1,$2,$3,$4,'👍')", project, other, m.ID, a.User.ID); err == nil {
		t.Fatal("reaction FK bypass")
	}
	setFlag := func(flag string, on bool) {
		if _, e := op.Exec(ctx, "UPDATE chat.project_settings SET flags=jsonb_set(flags,ARRAY[$2],to_jsonb($3::boolean)) WHERE project_id=$1", project, flag, on); e != nil {
			t.Fatal(e)
		}
	}
	setFlag("allow_reactions", false)
	for _, add := range []bool{true, false} {
		if _, err = svc.React(ctx, a, m.ID, c, "❤", add); !errors.Is(err, policy.ErrFeatureDisabled) {
			t.Fatal("reaction flag", err)
		}
	}
	setFlag("allow_reactions", true)
	results = make(chan error, 8)
	for range 8 {
		wg.Go(func() { _, e := svc.SetPin(ctx, a, c, m.ID, true); results <- e })
	}
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal(e)
		}
	}
	current, err = svc.Get(ctx, a, m.ID)
	if err != nil || current.Version != 5 || current.Pin == nil || current.Pin.PinnedBy != a.User.ID {
		t.Fatal("duplicate pins", err)
	}
	setFlag("allow_pin", false)
	for _, add := range []bool{true, false} {
		if _, err = svc.SetPin(ctx, a, c, m.ID, add); !errors.Is(err, policy.ErrFeatureDisabled) {
			t.Fatal("pin flag", err)
		}
	}
	// Flags prohibit mutations without concealing persisted read state.
	pins, err := svc.Pins(ctx, b, c)
	if err != nil || len(pins.Items) != 1 {
		t.Fatal("pin read", err)
	}
	setFlag("allow_pin", true)
	current, err = svc.SetPin(ctx, a, c, m.ID, false)
	if err != nil || current.Version != 6 || current.Pin != nil {
		t.Fatal("unpin", err)
	}
	current, err = svc.SetPin(ctx, a, c, m.ID, false)
	if err != nil || current.Version != 6 {
		t.Fatal("unpin retry", err)
	}
	// Post-write decryption failure must roll back the relation and version/event.
	broken := &message.Service{Store: &messagerepo.Store{DB: db, Crypto: failOpen{keys}}}
	if _, err = broken.React(ctx, a, m.ID, c, "👍", true); err == nil {
		t.Fatal("injection did not fail")
	}
	if _, err = broken.SetPin(ctx, a, c, m.ID, true); err == nil {
		t.Fatal("pin injection did not fail")
	}
	current, err = svc.Get(ctx, a, m.ID)
	if err != nil || current.Version != 6 || current.Pin != nil || len(current.Reactions) != 1 {
		t.Fatal("partial relation committed", err)
	}
	if _, err = svc.SetPin(ctx, a, c, m.ID, true); err != nil {
		t.Fatal(err)
	}
	// Snapshot includes old pins even when their messages are outside the last 50.
	for range 51 {
		send(c)
	}
	snapshot, err := recovery.Snapshot(ctx, b, c)
	if err != nil || len(snapshot.Messages) != 50 || len(snapshot.Pins) != 1 || snapshot.Pins[0] != m.ID {
		t.Fatal("old pin lost", err)
	}
	history, err := svc.History(ctx, b, c, message.Query{Forward: true, Limit: 100})
	if err != nil || history[0].Pin == nil || len(history[0].Reactions) != 1 {
		t.Fatal("history relations", err)
	}
	hits, err := svc.Search(ctx, b, c, "relationsbody", message.Query{Limit: 100})
	if err != nil || hits[len(hits)-1].Pin == nil {
		t.Fatal("search relations", err)
	}
	// Deletion races relation writes under the same conversation/message locks.
	results = make(chan error, 3)
	wg.Go(func() { _, e := svc.React(ctx, b, m.ID, c, "👍", true); results <- e })
	wg.Go(func() { _, e := svc.SetPin(ctx, a, c, m.ID, true); results <- e })
	wg.Go(func() { _, e := svc.Delete(ctx, a, m.ID, c); results <- e })
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil && !errors.Is(e, identity.ErrNotFound) {
			t.Fatal(e)
		}
	}
	current, err = svc.Get(ctx, a, m.ID)
	assertTombstone(t, current, err, "deleted")
	if current.Pin != nil || len(current.Reactions) != 0 {
		t.Fatal("tombstone associations")
	}
	var rows int
	if err = op.QueryRow(ctx, "SELECT (SELECT count(*) FROM chat.message_reactions WHERE message_id=$1)+(SELECT count(*) FROM chat.pinned_messages WHERE message_id=$1)", m.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("relations not removed", rows, err)
	}
	replay, err := recovery.Replay(ctx, a, c, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range replay.Events {
		if e.Payload["message_id"] != m.ID {
			continue
		}
		seen[e.Type] = true
		hydrated, ok := e.Payload["message"].(message.Message)
		if !ok || hydrated.Pin != nil || len(hydrated.Reactions) != 0 || hydrated.Status != "deleted" {
			t.Fatal("replay resurrected relation", e.Type)
		}
	}
	for _, kind := range []string{"reaction.created", "reaction.deleted", "message.pinned", "message.unpinned", "message.deleted"} {
		if !seen[kind] {
			t.Fatal("missing event", kind)
		}
	}
	var leaked bool
	if err = op.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM chat.outbox_events WHERE project_id=$1 AND envelope->'payload' ? 'message')", project).Scan(&leaked); err != nil || leaked {
		t.Fatal("stored hydrated data", err)
	}
	// Terminal removals remain idempotent; adds must not recreate associations.
	if _, err = svc.React(ctx, a, m.ID, c, "❤", true); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("reacted to tombstone", err)
	}
	if _, err = svc.SetPin(ctx, a, c, m.ID, true); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("pinned tombstone", err)
	}
	if _, err = svc.React(ctx, a, m.ID, c, "❤", false); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.SetPin(ctx, a, c, m.ID, false); err != nil {
		t.Fatal(err)
	}
	// Retention redacts associations immediately without needing physical cleanup.
	expired := send(other)
	if _, err = svc.SetPin(ctx, a, other, expired.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.React(ctx, b, expired.ID, other, "👍", true); err != nil {
		t.Fatal(err)
	}
	if _, err = op.Exec(ctx, "UPDATE chat.messages SET created_at=now()-interval '2 days',expires_at=now()-interval '1 day' WHERE id=$1", expired.ID); err != nil {
		t.Fatal(err)
	}
	current, err = svc.Get(ctx, b, expired.ID)
	assertTombstone(t, current, err, "expired")
	if current.Pin != nil || len(current.Reactions) != 0 {
		t.Fatal("expired associations")
	}
	pins, err = svc.Pins(ctx, b, other)
	if err != nil || len(pins.Items) != 0 {
		t.Fatal("expired pin listed", err)
	}
	// Hard DTO limits are enforced for new associations, while retries and other
	// users selecting an existing emoji remain successful at the boundary.
	bounded := send(other)
	for i := 0; i < 32; i++ {
		if _, err = svc.React(ctx, a, bounded.ID, other, string(rune(0x1f600+i)), true); err != nil {
			t.Fatal("reaction cap fixture", i, err)
		}
	}
	if _, err = svc.React(ctx, a, bounded.ID, other, "🦊", true); !errors.Is(err, policy.ErrInvalid) {
		t.Fatal("reaction cap bypass", err)
	}
	if _, err = svc.React(ctx, b, bounded.ID, other, "😀", true); err != nil {
		t.Fatal("existing emoji blocked at cap", err)
	}
	capConv := create("GROUP")
	var lastPin message.Message
	for range 100 {
		lastPin = send(capConv)
		if _, err = svc.SetPin(ctx, a, capConv, lastPin.ID, true); err != nil {
			t.Fatal("pin cap fixture", err)
		}
	}
	if _, err = svc.SetPin(ctx, a, capConv, lastPin.ID, true); err != nil {
		t.Fatal("pin retry at cap", err)
	}
	overflow := send(capConv)
	if _, err = svc.SetPin(ctx, a, capConv, overflow.ID, true); !errors.Is(err, policy.ErrInvalid) {
		t.Fatal("pin cap bypass", err)
	}
	if _, err = svc.SetPin(ctx, a, capConv, lastPin.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.SetPin(ctx, a, capConv, overflow.ID, true); err != nil {
		t.Fatal("unpin failed to free capacity", err)
	}
	pins, err = svc.Pins(ctx, a, capConv)
	if err != nil || len(pins.Items) != 100 {
		t.Fatal("pin collection limit", err)
	}
	// A role demotion takes effect even when the caller still has its old actor.
	if _, err = op.Exec(ctx, "UPDATE chat.conversation_members SET role='member' WHERE project_id=$1 AND conversation_id=$2 AND user_id=$3", project, capConv, a.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.SetPin(ctx, a, capConv, overflow.ID, false); !errors.Is(err, policy.ErrForbidden) {
		t.Fatal("demoted moderator unpinned", err)
	}
	// Membership and Project bans apply to removals/retries too.
	if _, err = op.Exec(ctx, "UPDATE chat.conversation_members SET banned=true WHERE project_id=$1 AND conversation_id=$2 AND user_id=$3", project, c, a.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.React(ctx, a, m.ID, c, "❤", false); !errors.Is(err, identity.ErrBanned) {
		t.Fatal("member ban", err)
	}
	if _, err = svc.SetPin(ctx, a, c, m.ID, false); !errors.Is(err, identity.ErrBanned) {
		t.Fatal("pin member ban", err)
	}
	if _, err = op.Exec(ctx, "UPDATE chat.users SET banned_at=now() WHERE id=$1", b.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.React(ctx, b, d.ID, direct, "👍", true); !errors.Is(err, identity.ErrBanned) {
		t.Fatal("project ban", err)
	}
	// Count serialization is stable for clients beyond JS's safe integer range.
	raw, err := json.Marshal(message.Reaction{Emoji: "👍", Count: 9007199254740993})
	if err != nil || string(raw) != `{"reaction":"👍","count":"9007199254740993"}` {
		t.Fatal("counter format", err)
	}
}
