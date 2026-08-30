//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/F1reStyLe/AlUrMessenger/internal/conversation"
	conversationrepo "github.com/F1reStyLe/AlUrMessenger/internal/conversation/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/cryptography"
	"github.com/F1reStyLe/AlUrMessenger/internal/event"
	eventrepo "github.com/F1reStyLe/AlUrMessenger/internal/event/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/message"
	messagerepo "github.com/F1reStyLe/AlUrMessenger/internal/message/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/outbox"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/F1reStyLe/AlUrMessenger/internal/provision"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// capturePublisher models failed acknowledgement and redelivery; SQL/outbox remain real.
type capturePublisher struct {
	fail   bool
	events []event.Envelope
}

// failOpen injects failure after inserts/outbox append, before the transaction commits.
type failOpen struct{ message.Crypto }

func (f failOpen) Open(string, string, string, cryptography.Envelope) ([]byte, error) {
	return nil, cryptography.ErrCrypto
}

func (p *capturePublisher) Publish(_ context.Context, key string, data []byte) error {
	var e event.Envelope
	if err := json.Unmarshal(data, &e); err != nil {
		return err
	}
	if key != e.AggregateID {
		return errors.New("wrong partition key")
	}
	p.events = append(p.events, e)
	if p.fail {
		return errors.New("ack lost")
	}
	return nil
}

// testMessages attacks transaction boundaries with the restricted runtime DB role.
func testMessages(t *testing.T, db *pgxpool.Pool, op *pgx.Conn) {
	ctx := t.Context()
	project := uuid.NewString()
	if err := provision.Create(ctx, op, provision.Project{ID: project, Name: "Messages", Issuer: "https://issuer.test", Audience: "chat"}); err != nil {
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
	c, _, err := convs.Create(ctx, a, conversation.Create{Type: "GROUP", MemberIDs: []string{b.User.ID}}, trace)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := cryptography.Generate()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := cryptography.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	svc := &message.Service{Store: &messagerepo.Store{DB: db, Crypto: keys}}
	p := message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: "Секрет Café"}, Metadata: map[string]any{"private": "Hidden metadata"}}
	results := make(chan message.Sent, 12)
	errs := make(chan error, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() { r, e := svc.Send(ctx, a, c.ID, p); results <- r; errs <- e })
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var first message.Sent
	created := 0
	for r := range results {
		if first.Message.ID != "" && first.Message.ID != r.Message.ID {
			t.Fatal("duplicate message")
		}
		first = r
		if !r.Deduplicated {
			created++
		}
		if r.Message.Sequence != 1 {
			t.Fatal("duplicate allocated sequence")
		}
	}
	if created != 1 {
		t.Fatal("expected one commit", created)
	}
	changed := p
	changed.Content.Text = "changed"
	if _, err = svc.Send(ctx, a, c.ID, changed); err != message.ErrIdempotencyConflict {
		t.Fatal("idempotency conflict", err)
	}
	// Reusing a client ID in another accessible conversation is also a conflict.
	second, _, err := convs.Create(ctx, a, conversation.Create{Type: "GROUP"}, trace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Send(ctx, a, second.ID, p); err != message.ErrIdempotencyConflict {
		t.Fatal("cross-conversation retry", err)
	}
	for range 10 {
		wg.Go(func() {
			command := p
			command.ClientID = uuid.NewString()
			_, e := svc.Send(ctx, b, c.ID, command)
			if e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	var count, seq, events, dedup int
	if err = op.QueryRow(ctx, `SELECT (SELECT count(*) FROM chat.messages WHERE conversation_id=$1),message_sequence,event_sequence,(SELECT count(*) FROM chat.message_idempotency WHERE conversation_id=$1) FROM chat.conversations WHERE id=$1`, c.ID).Scan(&count, &seq, &events, &dedup); err != nil {
		t.Fatal(err)
	}
	if count != 11 || seq != 11 || events != 12 || dedup != 11 {
		t.Fatal("atomic counters", count, seq, events, dedup)
	}
	// Payload, search and outbox must not contain either plaintext or metadata copies.
	var cipher, search, eventData []byte
	if err = op.QueryRow(ctx, `SELECT m.encrypted_content,to_json(s.tokens)::text::bytea,(SELECT string_agg(envelope::text,'')::bytea FROM chat.outbox_events WHERE conversation_id=$1) FROM chat.messages m JOIN chat.message_search s ON s.message_id=m.id WHERE m.id=$2`, c.ID, first.Message.ID).Scan(&cipher, &search, &eventData); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{cipher, search, eventData} {
		for _, plain := range []string{"Секрет", "Café", "Hidden metadata"} {
			if bytes.Contains(data, []byte(plain)) {
				t.Fatal("plaintext persisted outside encryption")
			}
		}
	}
	found, err := svc.Search(ctx, b, c.ID, "CAFÉ секрет", message.Query{Limit: 50})
	if err != nil || len(found) != 11 {
		t.Fatal("blind search", len(found), err)
	}
	if _, err = svc.Get(ctx, outsider, first.Message.ID); err != identity.ErrNotFound {
		t.Fatal("outsider history", err)
	}
	foreign := b
	foreign.ProjectID = uuid.NewString()
	if _, err = svc.Get(ctx, foreign, first.Message.ID); err != identity.ErrNotFound {
		t.Fatal("foreign project", err)
	}
	history, err := svc.History(ctx, b, c.ID, message.Query{After: 0, Forward: true, Limit: 50})
	if err != nil || len(history) != 11 {
		t.Fatal("history", err)
	}
	for i, m := range history {
		if m.Sequence != int64(i+1) {
			t.Fatal("history not contiguous")
		}
	}
	channel, _, err := convs.Create(ctx, a, conversation.Create{Type: "CHANNEL", MemberIDs: []string{b.User.ID}}, trace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Send(ctx, b, channel.ID, p); err != policy.ErrForbidden {
		t.Fatal("channel member wrote", err)
	}
	system := p
	system.Type = "SYSTEM"
	system.ClientID = uuid.NewString()
	if _, err = svc.Send(ctx, a, c.ID, system); err != policy.ErrInvalid {
		t.Fatal("public SYSTEM", err)
	}
	if _, err = svc.SendSystem(ctx, a, c.ID, system); err != policy.ErrForbidden {
		t.Fatal("forged system", err)
	}
	memberships := &conversation.MembershipService{Store: &conversationrepo.Store{DB: db}}
	if err = memberships.Remove(ctx, b, c.ID, b.User.ID, trace); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.History(ctx, b, c.ID, message.Query{Limit: 50}); err != identity.ErrNotFound {
		t.Fatal("left member read", err)
	}
	// Losing a broker acknowledgement keeps the head pending and prevents overtaking.
	publisher := &capturePublisher{fail: true}
	worker := &outbox.Worker{DB: db, Publisher: publisher}
	if ok, e := worker.One(ctx, project, c.ID); ok || e == nil {
		t.Fatal("failed ack marked published")
	}
	if ok, e := worker.One(ctx, project, c.ID); ok || e != nil {
		t.Fatal("backoff bypassed", e)
	}
	if len(publisher.events) != 1 {
		t.Fatal("head overtaken")
	}
	if _, err = op.Exec(ctx, "UPDATE chat.outbox_events SET next_attempt_at=now() WHERE conversation_id=$1", c.ID); err != nil {
		t.Fatal(err)
	}
	publisher.fail = false
	for {
		ok, e := worker.One(ctx, project, c.ID)
		if e != nil {
			t.Fatal(e)
		}
		if !ok {
			break
		}
	}
	if len(publisher.events) != 14 || publisher.events[0].ID != publisher.events[1].ID {
		t.Fatal("lost-ack redelivery", len(publisher.events))
	}
	for i, e := range publisher.events[1:] {
		if e.Sequence != int64(i+1) {
			t.Fatal("outbox order")
		}
	}
	// A failed outbox append rolls back the allocated counter and every message row.
	broken := &message.Service{Store: &messagerepo.Store{DB: db, Crypto: failOpen{keys}}}
	command := p
	command.ClientID = uuid.NewString()
	if _, err = broken.Send(ctx, a, second.ID, command); err == nil {
		t.Fatal("injected post-write failure accepted")
	}
	if err = op.QueryRow(ctx, "SELECT message_sequence,(SELECT count(*) FROM chat.messages WHERE conversation_id=$1) FROM chat.conversations WHERE id=$1", second.ID).Scan(&seq, &count); err != nil || seq != 0 || count != 0 {
		t.Fatal("message transaction did not roll back", seq, count, err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = eventrepo.Append(ctx, tx, project, second.ID, "fixture.rollback", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	tx.Rollback(ctx)
	if err = op.QueryRow(ctx, "SELECT event_sequence FROM chat.conversations WHERE id=$1", second.ID).Scan(&seq); err != nil || seq != 1 {
		t.Fatal("event rollback", seq, err)
	}
}
