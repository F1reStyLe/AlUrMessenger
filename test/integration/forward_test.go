//go:build integration

package integration

import (
	"bytes"
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

// testForward proves that forwarding is a new target-owned encrypted snapshot,
// not a read-time join to a source that may later change or disappear.
func testForward(t *testing.T, db *pgxpool.Pool, op *pgx.Conn) {
	ctx := t.Context()
	project := uuid.NewString()
	if err := provision.Create(ctx, op, provision.Project{ID: project, Name: "Forward", Issuer: "https://issuer.test", Audience: "chat"}); err != nil {
		t.Fatal(err)
	}
	actor := func(name string) identity.Actor {
		a := identity.Actor{ProjectID: project, User: identity.User{ID: uuid.NewString(), Kind: "human", Role: "user"}}
		if _, err := op.Exec(ctx, "INSERT INTO chat.users(id,project_id,external_user_id,display_name) VALUES($1::uuid,$2,$1::text,$3)", a.User.ID, project, name); err != nil {
			t.Fatal(err)
		}
		return a
	}
	a, b, outsider := actor("Alice"), actor("Original Bob"), actor("Outside")
	convs := &conversation.Service{Store: &conversationrepo.Store{DB: db}}
	trace := policy.Trace{RequestID: uuid.NewString()}
	create := func(members ...string) string {
		c, _, err := convs.Create(ctx, a, conversation.Create{Type: "GROUP", MemberIDs: members}, trace)
		if err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	sourceConversation, target := create(b.User.ID), create(b.User.ID)
	cfg, err := cryptography.Generate()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := cryptography.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	svc := &message.Service{Store: &messagerepo.Store{DB: db, Crypto: keys}}
	sourceResult, err := svc.Send(ctx, b, sourceConversation, message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: "independent snapshot"}, Metadata: map[string]any{"secret": "not forwarded"}})
	if err != nil {
		t.Fatal(err)
	}
	source := sourceResult.Message
	command := message.Send{ClientID: uuid.NewString(), ForwardFrom: &source.ID}
	result, err := svc.Send(ctx, a, target, command)
	if err != nil {
		t.Fatal(err)
	}
	forwarded := result.Message
	if forwarded.ConversationID != target || forwarded.SenderID != a.User.ID || forwarded.Type != "TEXT" || forwarded.Content == nil || forwarded.Content.Text != source.Content.Text {
		t.Fatal("wrong target snapshot", forwarded)
	}
	if forwarded.Forward == nil || forwarded.Forward.MessageID != source.ID || forwarded.Forward.OriginalSender.ID != b.User.ID || forwarded.Forward.OriginalSender.DisplayName != "Original Bob" {
		t.Fatal("wrong attribution", forwarded.Forward)
	}
	if len(forwarded.Metadata) != 0 || forwarded.Reply != nil || len(forwarded.Reactions) != 0 || forwarded.Pin != nil {
		t.Fatal("source associations leaked into snapshot")
	}
	// Ciphertext and nonce must be newly sealed in the target AAD context.
	var sourceCipher, targetCipher, sourceNonce, targetNonce []byte
	if err = op.QueryRow(ctx, "SELECT encrypted_content,nonce FROM chat.messages WHERE project_id=$1 AND id=$2", project, source.ID).Scan(&sourceCipher, &sourceNonce); err != nil {
		t.Fatal(err)
	}
	if err = op.QueryRow(ctx, "SELECT encrypted_content,nonce FROM chat.messages WHERE project_id=$1 AND id=$2", project, forwarded.ID).Scan(&targetCipher, &targetNonce); err != nil {
		t.Fatal(err)
	}
	if string(sourceCipher) == string(targetCipher) || string(sourceNonce) == string(targetNonce) {
		t.Fatal("forward reused encrypted material")
	}
	if found, e := svc.Search(ctx, a, target, "independent", message.Query{Limit: 10}); e != nil || len(found) != 1 {
		t.Fatal("forward search projection", e)
	}
	var storedEvent []byte
	if err = op.QueryRow(ctx, "SELECT envelope FROM chat.conversation_events WHERE project_id=$1 AND conversation_id=$2 AND envelope->>'event_type'='message.created' ORDER BY sequence DESC LIMIT 1", project, target).Scan(&storedEvent); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"independent snapshot", "Original Bob", sourceConversation} {
		if bytes.Contains(storedEvent, []byte(secret)) {
			t.Fatal("forward event leaked snapshot data")
		}
	}
	// Matching concurrent retries converge on the already committed target row.
	var wg sync.WaitGroup
	results := make(chan message.Sent, 6)
	errs := make(chan error, 6)
	for range 6 {
		wg.Go(func() { got, e := svc.Send(ctx, a, target, command); results <- got; errs <- e })
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for got := range results {
		if got.Message.ID != forwarded.ID || !got.Deduplicated {
			t.Fatal("forward dedup diverged")
		}
	}
	otherSource, err := svc.Send(ctx, b, sourceConversation, message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: "other"}})
	if err != nil {
		t.Fatal(err)
	}
	changed := command
	changed.ForwardFrom = &otherSource.Message.ID
	if _, err = svc.Send(ctx, a, target, changed); !errors.Is(err, message.ErrIdempotencyConflict) {
		t.Fatal("changed source reused client id", err)
	}
	if _, err = svc.Send(ctx, outsider, target, message.Send{ClientID: uuid.NewString(), ForwardFrom: &source.ID}); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("outsider forwarded", err)
	}
	if _, err = op.Exec(ctx, "UPDATE chat.project_settings SET flags=jsonb_set(flags,'{allow_forward}','false') WHERE project_id=$1", project); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Send(ctx, a, target, command); !errors.Is(err, policy.ErrFeatureDisabled) {
		t.Fatal("matching retry bypassed flag", err)
	}
	if _, err = op.Exec(ctx, "UPDATE chat.project_settings SET flags=jsonb_set(flags,'{allow_forward}','true') WHERE project_id=$1", project); err != nil {
		t.Fatal(err)
	}
	var beforeMessages, beforeEvents int
	var beforeSequence int64
	if err = op.QueryRow(ctx, "SELECT message_sequence,(SELECT count(*) FROM chat.messages WHERE project_id=$1 AND conversation_id=$2),(SELECT count(*) FROM chat.conversation_events WHERE project_id=$1 AND conversation_id=$2) FROM chat.conversations WHERE project_id=$1 AND id=$2", project, target).Scan(&beforeSequence, &beforeMessages, &beforeEvents); err != nil {
		t.Fatal(err)
	}
	broken := &message.Service{Store: &messagerepo.Store{DB: db, Crypto: &failSecondOpen{Crypto: keys}}}
	if _, err = broken.Send(ctx, a, target, message.Send{ClientID: uuid.NewString(), ForwardFrom: &otherSource.Message.ID}); !errors.Is(err, cryptography.ErrCrypto) {
		t.Fatal("post-write crypto failure not injected", err)
	}
	var afterMessages, afterEvents int
	var afterSequence int64
	if err = op.QueryRow(ctx, "SELECT message_sequence,(SELECT count(*) FROM chat.messages WHERE project_id=$1 AND conversation_id=$2),(SELECT count(*) FROM chat.conversation_events WHERE project_id=$1 AND conversation_id=$2) FROM chat.conversations WHERE project_id=$1 AND id=$2", project, target).Scan(&afterSequence, &afterMessages, &afterEvents); err != nil || beforeSequence != afterSequence || beforeMessages != afterMessages || beforeEvents != afterEvents {
		t.Fatal("failed forward partially committed", err)
	}
	// Source edits and both logical and physical deletion cannot rewrite or break
	// the independent target snapshot. The FK clears only its nullable header.
	if _, err = svc.Edit(ctx, b, source.ID, "", message.Edit{Content: message.Content{Text: "changed source"}, ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Delete(ctx, b, source.ID, ""); err != nil {
		t.Fatal(err)
	}
	// Phase 10 cleanup expires the dedup receipt before physically purging the
	// source row; remove that independent retention guard in the same order here.
	if _, err = op.Exec(ctx, "DELETE FROM chat.message_idempotency WHERE project_id=$1 AND message_id=$2", project, source.ID); err != nil {
		t.Fatal("source receipt cleanup", err)
	}
	if _, err = op.Exec(ctx, "DELETE FROM chat.messages WHERE project_id=$1 AND id=$2", project, source.ID); err != nil {
		t.Fatal("physical source purge", err)
	}
	current, err := svc.Get(ctx, a, forwarded.ID)
	if err != nil || current.Content == nil || current.Content.Text != "independent snapshot" || current.Forward == nil || current.Forward.OriginalSender.DisplayName != "Original Bob" {
		t.Fatal("source lifecycle changed forward", current, err)
	}
	var header *string
	if err = op.QueryRow(ctx, "SELECT forwarded_from_message_id::text FROM chat.messages WHERE project_id=$1 AND id=$2", project, forwarded.ID).Scan(&header); err != nil || header != nil {
		t.Fatal("source purge did not clear header", header, err)
	}
}
