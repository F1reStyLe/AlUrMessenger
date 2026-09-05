//go:build integration

package integration

import (
	"bytes"
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

// failSecondOpen lets Edit replace ciphertext/search, then fails its final read.
// The caller must observe a rollback of both content and index, not a partial edit.
type failSecondOpen struct {
	message.Crypto
	calls int
}

func (f *failSecondOpen) Open(p, c, m string, e cryptography.Envelope) ([]byte, error) {
	f.calls++
	if f.calls == 2 {
		return nil, cryptography.ErrCrypto
	}
	return f.Crypto.Open(p, c, m, e)
}

// testMessageFeatures uses restricted runtime SQL and real transactions, including
// simultaneous writers, tenant boundaries and redaction across every read path.
func testMessageFeatures(t *testing.T, db *pgxpool.Pool, op *pgx.Conn) {
	ctx := t.Context()
	project := uuid.NewString()
	if err := provision.Create(ctx, op, provision.Project{ID: project, Name: "Message features", Issuer: "https://issuer.test", Audience: "chat"}); err != nil {
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
	create := func() string {
		c, _, err := convs.Create(ctx, a, conversation.Create{Type: "GROUP", MemberIDs: []string{b.User.ID}}, trace)
		if err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	c, other := create(), create()
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
	command := func(text string) message.Send {
		return message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: text}, Metadata: map[string]any{"private": "encrypted metadata"}}
	}
	send := func(who identity.Actor, conv string, p message.Send) message.Message {
		r, e := svc.Send(ctx, who, conv, p)
		if e != nil {
			t.Fatal(e)
		}
		return r.Message
	}
	setFlag := func(name string, enabled bool) {
		if _, e := op.Exec(ctx, "UPDATE chat.project_settings SET flags=jsonb_set(flags,ARRAY[$2],to_jsonb($3::boolean)) WHERE project_id=$1", project, name, enabled); e != nil {
			t.Fatal(e)
		}
	}
	parentCommand := command("originalword")
	parent := send(a, c, parentCommand)
	replyCommand := command("replyword")
	replyCommand.ReplyTo = &parent.ID
	reply := send(b, c, replyCommand)
	if reply.Reply == nil || reply.Reply.MessageID != parent.ID {
		t.Fatal("missing reply reference")
	}
	if _, e := svc.Send(ctx, b, other, replyCommand); !errors.Is(e, message.ErrIdempotencyConflict) {
		t.Fatal("dedup conversation mismatch", e)
	}
	foreign := command("foreign")
	foreign.ReplyTo = &parent.ID
	if _, e := svc.Send(ctx, b, other, foreign); !errors.Is(e, identity.ErrNotFound) {
		t.Fatal("cross-conversation reply", e)
	}
	// Composite DB constraints remain a second boundary even if application checks regress.
	otherMessage := send(a, other, command("elsewhere"))
	if _, e := db.Exec(ctx, "UPDATE chat.messages SET reply_to_message_id=$3 WHERE project_id=$1 AND id=$2", project, reply.ID, otherMessage.ID); e == nil {
		t.Fatal("DB accepted cross-conversation reply")
	}
	setFlag("allow_reply", false)
	if _, e := svc.Send(ctx, b, c, replyCommand); !errors.Is(e, policy.ErrFeatureDisabled) {
		t.Fatal("retry bypassed reply flag", e)
	}
	setFlag("allow_reply", true)
	edit := message.Edit{Content: message.Content{Text: "editedword"}, ExpectedVersion: 1}
	for _, who := range []identity.Actor{b, outsider} {
		if _, e := svc.Edit(ctx, who, parent.ID, "", edit); e == nil {
			t.Fatal("non-author edit")
		}
		if _, e := svc.Delete(ctx, who, parent.ID, ""); e == nil {
			t.Fatal("non-author delete")
		}
	}
	foreignActor := a
	foreignActor.ProjectID = uuid.NewString()
	if _, e := svc.Edit(ctx, foreignActor, parent.ID, "", edit); !errors.Is(e, identity.ErrNotFound) {
		t.Fatal("foreign tenant", e)
	}
	if _, e := svc.Edit(ctx, a, parent.ID, other, edit); !errors.Is(e, identity.ErrNotFound) {
		t.Fatal("WS scope ignored", e)
	}
	if _, e := svc.Delete(ctx, a, parent.ID, other); !errors.Is(e, identity.ErrNotFound) {
		t.Fatal("WS delete scope ignored", e)
	}
	if _, e := svc.Delete(ctx, a, reply.ID, ""); !errors.Is(e, policy.ErrForbidden) {
		t.Fatal("moderator bypassed author-only delete", e)
	}
	setFlag("allow_edit", false)
	if _, e := svc.Edit(ctx, a, parent.ID, "", edit); !errors.Is(e, policy.ErrFeatureDisabled) {
		t.Fatal("edit flag", e)
	}
	setFlag("allow_edit", true)
	// Two edits of one version have exactly one winner, even on separate pool connections.
	errorsCh := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { _, e := svc.Edit(ctx, a, parent.ID, "", edit); errorsCh <- e })
	}
	wg.Wait()
	close(errorsCh)
	winners, conflicts := 0, 0
	for e := range errorsCh {
		if e == nil {
			winners++
		} else if errors.Is(e, policy.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatal("lost optimistic update", winners, conflicts)
	}
	updated, e := svc.Get(ctx, a, parent.ID)
	if e != nil || updated.Version != 2 || updated.Sequence != parent.Sequence || updated.EditedAt == nil || updated.Content.Text != "editedword" {
		t.Fatal("edit projection", e)
	}
	query := message.Query{Limit: 100}
	if found, e := svc.Search(ctx, a, c, "originalword", query); e != nil || len(found) != 0 {
		t.Fatal("stale search index", e)
	}
	if found, e := svc.Search(ctx, a, c, "editedword", query); e != nil || len(found) != 1 {
		t.Fatal("new search missing", e)
	}
	duplicate, e := svc.Send(ctx, a, c, parentCommand)
	if e != nil || !duplicate.Deduplicated || duplicate.Message.Version != 2 || duplicate.Message.Content.Text != "editedword" {
		t.Fatal("retry resurrected old edit", e)
	}
	broken := &message.Service{Store: &messagerepo.Store{DB: db, Crypto: &failSecondOpen{Crypto: keys}}}
	if _, e = broken.Edit(ctx, a, parent.ID, "", message.Edit{Content: message.Content{Text: "rollbackword"}, ExpectedVersion: 2}); !errors.Is(e, cryptography.ErrCrypto) {
		t.Fatal("injection did not fail", e)
	}
	if got, e := svc.Get(ctx, a, parent.ID); e != nil || got.Version != 2 || got.Content.Text != "editedword" {
		t.Fatal("partial edit committed", e)
	}
	if found, e := svc.Search(ctx, a, c, "rollbackword", query); e != nil || len(found) != 0 {
		t.Fatal("partial index committed", e)
	}
	setFlag("allow_delete", false)
	if _, e = svc.Delete(ctx, b, reply.ID, ""); !errors.Is(e, policy.ErrFeatureDisabled) {
		t.Fatal("delete flag", e)
	}
	setFlag("allow_delete", true)
	deleted, e := svc.Delete(ctx, b, reply.ID, "")
	assertTombstone(t, deleted, e, "deleted")
	conv, e := convs.Get(ctx, a, c)
	if e != nil || conv.LastMessageID == nil || *conv.LastMessageID != parent.ID {
		t.Fatal("last message not recalculated", e)
	}
	// Simultaneous retries must not add extra versions or events.
	versions := make(chan int64, 8)
	errorsCh = make(chan error, 8)
	for range 8 {
		wg.Go(func() { m, e := svc.Delete(ctx, b, reply.ID, ""); versions <- m.Version; errorsCh <- e })
	}
	wg.Wait()
	close(versions)
	close(errorsCh)
	for e := range errorsCh {
		if e != nil {
			t.Fatal(e)
		}
	}
	for version := range versions {
		if version != 2 {
			t.Fatal("delete not idempotent", version)
		}
	}
	deleted, e = svc.Delete(ctx, a, parent.ID, "")
	assertTombstone(t, deleted, e, "deleted")
	if deleted.Version != 3 {
		t.Fatal("wrong deletion version")
	}
	conv, e = convs.Get(ctx, a, c)
	if e != nil || conv.LastMessageID != nil || conv.MessageSequence != 2 {
		t.Fatal("empty last message/counter", e)
	}
	if _, e = svc.Edit(ctx, a, parent.ID, "", message.Edit{Content: message.Content{Text: "resurrect"}, ExpectedVersion: 3}); !errors.Is(e, identity.ErrNotFound) {
		t.Fatal("edited tombstone", e)
	}
	noKeys := &message.Service{Store: &messagerepo.Store{DB: db, Crypto: failOpen{keys}}}
	got, e := noKeys.Get(ctx, a, parent.ID)
	assertTombstone(t, got, e, "deleted")
	duplicate, e = svc.Send(ctx, a, c, parentCommand)
	assertTombstone(t, duplicate.Message, e, "deleted")
	if !duplicate.Deduplicated {
		t.Fatal("deleted retry created new message")
	}
	newReply := command("must fail")
	newReply.ReplyTo = &parent.ID
	if _, e = svc.Send(ctx, b, c, newReply); !errors.Is(e, identity.ErrNotFound) {
		t.Fatal("reply to deleted parent", e)
	}
	if found, e := svc.Search(ctx, a, c, "editedword", query); e != nil || len(found) != 0 {
		t.Fatal("deleted search hit", e)
	}
	var ciphertext, indexCount int
	if e = op.QueryRow(ctx, "SELECT octet_length(encrypted_content),(SELECT count(*) FROM chat.message_search WHERE project_id=$1 AND message_id=$2) FROM chat.messages WHERE project_id=$1 AND id=$2", project, parent.ID).Scan(&ciphertext, &indexCount); e != nil || ciphertext != 0 || indexCount != 0 {
		t.Fatal("body not wiped", e)
	}
	history, e := svc.History(ctx, a, c, query)
	if e != nil || len(history) != 2 {
		t.Fatal("tombstones lost in history", e)
	}
	for _, m := range history {
		assertTombstone(t, m, nil, "deleted")
	}
	snapshot, e := recovery.Snapshot(ctx, a, c)
	if e != nil || len(snapshot.Messages) != 2 {
		t.Fatal("snapshot", e)
	}
	for _, m := range snapshot.Messages {
		assertTombstone(t, m, nil, "deleted")
	}
	replay, e := recovery.Replay(ctx, a, c, 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	counts := map[string]int{}
	for _, event := range replay.Events {
		counts[event.Type]++
		if m, ok := event.Payload["message"].(message.Message); ok {
			assertTombstone(t, m, nil, "deleted")
		}
	}
	if counts["message.created"] != 2 || counts["message.updated"] != 1 || counts["message.deleted"] != 2 {
		t.Fatal("wrong durable events", counts)
	}
	var leaked bool
	if e = op.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chat.outbox_events WHERE project_id=$1 AND (envelope::text LIKE '%editedword%' OR envelope::text LIKE '%originalword%' OR envelope->'payload' ? 'message'))`, project).Scan(&leaked); e != nil || leaked {
		t.Fatal("hydration persisted in outbox", e)
	}
	// Explicit expiry reads return redacted identity while search/replies remain live-only.
	expiredCommand := command("expiredword")
	expired := send(a, c, expiredCommand)
	if _, e = op.Exec(ctx, "UPDATE chat.messages SET created_at=now()-interval '2 days',expires_at=now()-interval '1 day' WHERE project_id=$1 AND id=$2", project, expired.ID); e != nil {
		t.Fatal(e)
	}
	got, e = noKeys.Get(ctx, a, expired.ID)
	assertTombstone(t, got, e, "expired")
	duplicate, e = svc.Send(ctx, a, c, expiredCommand)
	assertTombstone(t, duplicate.Message, e, "expired")
	newReply.ReplyTo = &expired.ID
	if _, e = svc.Send(ctx, b, c, newReply); !errors.Is(e, identity.ErrNotFound) {
		t.Fatal("reply to expired parent", e)
	}
	// Runtime grants do not accidentally permit changing immutable headers or physical deletion.
	if _, e = db.Exec(ctx, "UPDATE chat.messages SET sequence=999 WHERE project_id=$1 AND id=$2", project, expired.ID); e == nil {
		t.Fatal("runtime changed sequence")
	}
	if _, e = db.Exec(ctx, "DELETE FROM chat.messages WHERE project_id=$1 AND id=$2", project, expired.ID); e == nil {
		t.Fatal("runtime physically deleted message")
	}
	// Banned users may read tombstones but cannot mutate their own surviving messages.
	if _, e = op.Exec(ctx, "UPDATE chat.conversation_members SET banned=true WHERE project_id=$1 AND conversation_id=$2 AND user_id=$3", project, other, a.User.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Edit(ctx, a, otherMessage.ID, "", edit); !errors.Is(e, identity.ErrBanned) {
		t.Fatal("membership ban edit", e)
	}
	if _, e = svc.Delete(ctx, a, otherMessage.ID, ""); !errors.Is(e, identity.ErrBanned) {
		t.Fatal("membership ban delete", e)
	}
	// A delete racing an edit can follow or precede it, but the final body must
	// always be gone and the event stream must reflect the serialized outcome.
	raceConv := create()
	raceMessage := send(a, raceConv, command("raceoriginal"))
	raceErrors := make(chan error, 2)
	wg.Go(func() {
		_, e := svc.Edit(ctx, a, raceMessage.ID, "", message.Edit{Content: message.Content{Text: "racededit"}, ExpectedVersion: 1})
		raceErrors <- e
	})
	wg.Go(func() { _, e := svc.Delete(ctx, a, raceMessage.ID, ""); raceErrors <- e })
	wg.Wait()
	close(raceErrors)
	for e := range raceErrors {
		if e != nil && !errors.Is(e, identity.ErrNotFound) {
			t.Fatal("edit/delete race", e)
		}
	}
	got, e = svc.Get(ctx, a, raceMessage.ID)
	assertTombstone(t, got, e, "deleted")
	if got.Version != 2 && got.Version != 3 {
		t.Fatal("invalid race version", got.Version)
	}
	if hits, e := svc.Search(ctx, a, raceConv, "racededit", query); e != nil || len(hits) != 0 {
		t.Fatal("race resurrected search", e)
	}
	// Project bans affect all conversations, while SYSTEM protection is based on
	// the persisted message type, not a caller-supplied transport assertion.
	protected := send(a, raceConv, command("system fixture"))
	if _, e = op.Exec(ctx, "UPDATE chat.messages SET type='SYSTEM' WHERE project_id=$1 AND id=$2", project, protected.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Edit(ctx, a, protected.ID, "", edit); !errors.Is(e, policy.ErrForbidden) {
		t.Fatal("SYSTEM edited", e)
	}
	if _, e = svc.Delete(ctx, a, protected.ID, ""); !errors.Is(e, policy.ErrForbidden) {
		t.Fatal("SYSTEM deleted through user API", e)
	}
	if _, e = op.Exec(ctx, "UPDATE chat.users SET banned_at=now() WHERE project_id=$1 AND id=$2", project, a.User.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Delete(ctx, a, raceMessage.ID, ""); !errors.Is(e, identity.ErrBanned) {
		t.Fatal("project ban bypass on repeated delete", e)
	}
}

// assertTombstone verifies both typed projection and actual JSON serialization.
func assertTombstone(t *testing.T, m message.Message, err error, status string) {
	t.Helper()
	if err != nil || m.ID == "" || m.Status != status || m.Content != nil || m.Metadata != nil || m.Reply != nil || m.Forward != nil {
		t.Fatal("invalid tombstone", m.ID, m.Status, err)
	}
	data, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	for _, key := range [][]byte{[]byte(`"content"`), []byte(`"metadata"`), []byte(`"reply"`), []byte(`"forward"`)} {
		if bytes.Contains(data, key) {
			t.Fatal("tombstone leaks", string(key))
		}
	}
}
