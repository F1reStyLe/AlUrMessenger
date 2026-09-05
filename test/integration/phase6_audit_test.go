//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"sync"
	"testing"

	"github.com/F1reStyLe/AlUrMessenger/internal/attachment"
	attachmentrepo "github.com/F1reStyLe/AlUrMessenger/internal/attachment/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/conversation"
	conversationrepo "github.com/F1reStyLe/AlUrMessenger/internal/conversation/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/cryptography"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/message"
	messagerepo "github.com/F1reStyLe/AlUrMessenger/internal/message/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/infrastructure"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/F1reStyLe/AlUrMessenger/internal/provision"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type failDeleteOnce struct {
	delegate attachmentrepo.ObjectRemover
	mu       sync.Mutex
	failed   bool
}

func (r *failDeleteOnce) Delete(ctx context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.failed {
		r.failed = true
		return errors.New("injected delete outage")
	}
	return r.delegate.Delete(ctx, key)
}

// testPhase6Audit exercises the boundaries that are easy to miss in happy-path
// tests: tenant/membership isolation, single-consumer concurrency, flags on an
// idempotent retry, transaction rollback and durable cleanup retry.
func testPhase6Audit(t *testing.T, clients *infrastructure.Clients, op *pgx.Conn) {
	ctx := t.Context()
	newProject := func(name string) string {
		id := uuid.NewString()
		if err := provision.Create(ctx, op, provision.Project{ID: id, Name: name, Issuer: "https://issuer.test", Audience: "chat"}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	newActor := func(project, name string) identity.Actor {
		a := identity.Actor{ProjectID: project, User: identity.User{ID: uuid.NewString(), Kind: "human", Role: "user"}}
		if _, err := op.Exec(ctx, "INSERT INTO chat.users(id,project_id,external_user_id,display_name) VALUES($1::uuid,$2,$1::text,$3)", a.User.ID, project, name); err != nil {
			t.Fatal(err)
		}
		return a
	}
	project, foreignProject := newProject("Phase 6 audit"), newProject("Foreign attachment tenant")
	alice, bob, outsider := newActor(project, "Alice"), newActor(project, "Bob"), newActor(project, "Outsider")
	foreign := newActor(foreignProject, "Foreign")
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	body := encoded.Bytes()
	uploadConfig := config.Upload{MaxBytes: 50 << 20, MaxDimension: 8192, MaxPixels: 16_000_000, DecodeConcurrency: 2}
	attachments := attachment.New(&attachmentrepo.Store{DB: clients.Postgres}, clients.Storage, uploadConfig)
	upload := func(actor identity.Actor, name string) attachment.Attachment {
		result, err := attachments.Upload(ctx, actor, attachment.Input{Name: name, DeclaredMIME: "image/png", Size: int64(len(body)), Reader: bytes.NewReader(body)})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := upload(alice, "audit.png")
	if _, err := attachments.Get(ctx, foreign, first.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("cross-Project attachment discovery", err)
	}
	conversations := &conversation.Service{Store: &conversationrepo.Store{DB: clients.Postgres}}
	trace := policy.Trace{RequestID: uuid.NewString()}
	target, _, err := conversations.Create(ctx, alice, conversation.Create{Type: "GROUP", MemberIDs: []string{bob.User.ID}}, trace)
	if err != nil {
		t.Fatal(err)
	}
	outsiderTarget, _, err := conversations.Create(ctx, outsider, conversation.Create{Type: "GROUP"}, trace)
	if err != nil {
		t.Fatal(err)
	}
	foreignTarget, _, err := conversations.Create(ctx, foreign, conversation.Create{Type: "GROUP"}, trace)
	if err != nil {
		t.Fatal(err)
	}
	keyConfig, err := cryptography.Generate()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := cryptography.New(keyConfig)
	if err != nil {
		t.Fatal(err)
	}
	messages := &message.Service{Store: &messagerepo.Store{DB: clients.Postgres, Crypto: keys}}
	invalidTarget := message.Send{ClientID: uuid.NewString(), Type: "IMAGE", Content: message.Content{}, AttachmentIDs: []string{first.ID}}
	if _, err = messages.Send(ctx, alice, outsiderTarget.ID, invalidTarget); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("nonmember consumed attachment", err)
	}
	if _, err = messages.Send(ctx, foreign, foreignTarget.ID, invalidTarget); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("foreign Project consumed attachment", err)
	}
	var ready string
	if err = op.QueryRow(ctx, "SELECT status FROM chat.attachments WHERE project_id=$1 AND id=$2", project, first.ID).Scan(&ready); err != nil || ready != "ready" {
		t.Fatal("failed authorization changed lifecycle", ready, err)
	}
	commands := []message.Send{
		{ClientID: uuid.NewString(), Type: "IMAGE", Content: message.Content{Caption: "single winner"}, AttachmentIDs: []string{first.ID}},
		{ClientID: uuid.NewString(), Type: "IMAGE", Content: message.Content{Caption: "single loser"}, AttachmentIDs: []string{first.ID}},
	}
	results := make(chan message.Sent, 2)
	errorsFound := make(chan error, 2)
	var wg sync.WaitGroup
	for _, command := range commands {
		wg.Add(1)
		go func(command message.Send) {
			defer wg.Done()
			result, sendErr := messages.Send(ctx, alice, target.ID, command)
			if sendErr != nil {
				errorsFound <- sendErr
				return
			}
			results <- result
		}(command)
	}
	wg.Wait()
	close(results)
	close(errorsFound)
	if len(results) != 1 || len(errorsFound) != 1 {
		t.Fatal("attachment had multiple or no consumers", len(results), len(errorsFound))
	}
	winner := <-results
	if sendErr := <-errorsFound; !errors.Is(sendErr, identity.ErrNotFound) {
		t.Fatal("losing consumer error", sendErr)
	}
	if winner.Message.Sequence != 1 || len(winner.Message.Attachments) != 1 {
		t.Fatal("failed consumer allocated sequence or lost relation", winner)
	}
	if found, e := messages.Search(ctx, bob, target.ID, winner.Message.Content.Caption, message.Query{Limit: 10}); e != nil || len(found) != 1 || found[0].ID != winner.Message.ID {
		t.Fatal("IMAGE caption search", e)
	}
	// Matching retries still pass feature policy; turning the flag off does not
	// hide already committed reads but rejects every new/replayed write command.
	if _, err = op.Exec(ctx, "UPDATE chat.project_settings SET flags=jsonb_set(flags,'{allow_images}','false') WHERE project_id=$1", project); err != nil {
		t.Fatal(err)
	}
	winningCommand := commands[0]
	if winner.Message.Content.Caption == "single loser" {
		winningCommand = commands[1]
	}
	if _, err = messages.Send(ctx, alice, target.ID, winningCommand); !errors.Is(err, policy.ErrFeatureDisabled) {
		t.Fatal("allow_images bypassed by matching retry", err)
	}
	if _, err = attachments.Get(ctx, bob, first.ID); err != nil {
		t.Fatal("flag hid existing attachment state", err)
	}
	if _, err = op.Exec(ctx, "UPDATE chat.project_settings SET flags=jsonb_set(flags,'{allow_images}','true') WHERE project_id=$1", project); err != nil {
		t.Fatal(err)
	}
	if _, err = messages.Edit(ctx, alice, winner.Message.ID, target.ID, message.Edit{Content: message.Content{Text: "illegal"}, ExpectedVersion: 1}); !errors.Is(err, policy.ErrForbidden) {
		t.Fatal("IMAGE body edited through TEXT endpoint", err)
	}

	// A storage outage after pending_delete is durable. The same job retries
	// without depending on in-memory ownership and then becomes a no-op.
	orphan := upload(alice, "expired.png")
	if _, err = op.Exec(ctx, "UPDATE chat.attachments SET expires_at=clock_timestamp()-interval '1 second' WHERE project_id=$1 AND id=$2", project, orphan.ID); err != nil {
		t.Fatal(err)
	}
	flaky := &failDeleteOnce{delegate: clients.Storage}
	cleaner := &attachmentrepo.Cleaner{DB: clients.Postgres, Blobs: flaky}
	if worked, cleanupErr := cleaner.One(ctx); !worked || cleanupErr == nil {
		t.Fatal("cleanup outage was not retained", worked, cleanupErr)
	}
	var lifecycle string
	if err = op.QueryRow(ctx, "SELECT status FROM chat.storage_objects WHERE project_id=$1 AND id=(SELECT object_id FROM chat.attachments WHERE project_id=$1 AND id=$2)", project, orphan.ID).Scan(&lifecycle); err != nil || lifecycle != "pending_delete" {
		t.Fatal("cleanup retry state missing", lifecycle, err)
	}
	if worked, cleanupErr := cleaner.One(ctx); !worked || cleanupErr != nil {
		t.Fatal("cleanup retry failed", worked, cleanupErr)
	}
	if worked, cleanupErr := cleaner.One(ctx); worked || cleanupErr != nil {
		t.Fatal("cleanup completion is not idempotent", worked, cleanupErr)
	}
}
