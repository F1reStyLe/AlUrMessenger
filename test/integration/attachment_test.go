//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

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
	"github.com/minio/minio-go/v7"
)

type failingBlobs struct{}

func (failingBlobs) Put(context.Context, string, io.Reader, int64, string) error {
	return errors.New("injected storage failure")
}
func (failingBlobs) Presign(context.Context, string, string, time.Duration) (string, error) {
	return "", errors.New("injected storage failure")
}

func testAttachmentUpload(t *testing.T, clients *infrastructure.Clients, op *pgx.Conn) {
	ctx := t.Context()
	project, user := uuid.NewString(), uuid.NewString()
	if err := provision.Create(ctx, op, provision.Project{ID: project, Name: "Attachments", Issuer: "https://issuer.test", Audience: "chat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Exec(ctx, "INSERT INTO chat.users(id,project_id,external_user_id,display_name) VALUES($1::uuid,$2,$1::text,'Uploader')", user, project); err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{ProjectID: project, User: identity.User{ID: user, Kind: "human", Role: "user"}}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	body := encoded.Bytes()
	cfg := config.Upload{MaxBytes: 50 << 20, MaxDimension: 8192, MaxPixels: 16_000_000, DecodeConcurrency: 2}
	repo := &attachmentrepo.Store{DB: clients.Postgres}
	service := attachment.New(repo, clients.Storage, cfg)
	result, err := service.Upload(ctx, actor, attachment.Input{Name: "safe.png", DeclaredMIME: "image/png", Size: int64(len(body)), Reader: bytes.NewReader(body)})
	if err != nil || result.Status != "ready" || result.Width != 3 || result.Height != 2 {
		t.Fatal(result, err)
	}
	if metadata, e := service.Get(ctx, actor, result.ID); e != nil || metadata.ID != result.ID {
		t.Fatal("uploader cannot read unattached metadata", e)
	}
	reader := identity.Actor{ProjectID: project, User: identity.User{ID: uuid.NewString(), Kind: "human", Role: "user"}}
	outsider := identity.Actor{ProjectID: project, User: identity.User{ID: uuid.NewString(), Kind: "human", Role: "user"}}
	for _, candidate := range []identity.Actor{reader, outsider} {
		if _, e := op.Exec(ctx, "INSERT INTO chat.users(id,project_id,external_user_id,display_name) VALUES($1::uuid,$2,$1::text,'Reader')", candidate.User.ID, project); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := service.Get(ctx, reader, result.ID); !errors.Is(e, identity.ErrNotFound) {
		t.Fatal("unattached upload leaked", e)
	}
	convs := &conversation.Service{Store: &conversationrepo.Store{DB: clients.Postgres}}
	trace := policy.Trace{RequestID: uuid.NewString()}
	sourceConversation, _, err := convs.Create(ctx, actor, conversation.Create{Type: "GROUP", MemberIDs: []string{reader.User.ID}}, trace)
	if err != nil {
		t.Fatal(err)
	}
	targetConversation, _, err := convs.Create(ctx, actor, conversation.Create{Type: "GROUP", MemberIDs: []string{reader.User.ID}}, trace)
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
	command := message.Send{ClientID: uuid.NewString(), Type: "IMAGE", Content: message.Content{Caption: "private image"}, AttachmentIDs: []string{result.ID}}
	sent, err := messages.Send(ctx, actor, sourceConversation.ID, command)
	if err != nil || sent.Message.Type != "IMAGE" || len(sent.Message.Attachments) != 1 || sent.Message.Attachments[0].ID != result.ID {
		t.Fatal("IMAGE association failed", sent, err)
	}
	if retry, e := messages.Send(ctx, actor, sourceConversation.ID, command); e != nil || !retry.Deduplicated || retry.Message.ID != sent.Message.ID {
		t.Fatal("IMAGE retry consumed attachment twice", e)
	}
	if _, e := service.Get(ctx, reader, result.ID); e != nil {
		t.Fatal("current member cannot read attached metadata", e)
	}
	if _, e := service.Get(ctx, outsider, result.ID); !errors.Is(e, identity.ErrNotFound) {
		t.Fatal("outsider read attached metadata", e)
	}
	download, err := service.Download(ctx, reader, result.ID)
	if err != nil || download.ExpiresAt.Before(time.Now().Add(50*time.Second)) || download.ExpiresAt.After(time.Now().Add(70*time.Second)) {
		t.Fatal("invalid download capability", download, err)
	}
	response, err := http.Get(download.URL)
	if err != nil {
		t.Fatal(err)
	}
	downloaded, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || !bytes.Equal(downloaded, body) {
		t.Fatal("signed object download failed", response.StatusCode, readErr)
	}
	forwardCommand := message.Send{ClientID: uuid.NewString(), ForwardFrom: &sent.Message.ID}
	forwarded, err := messages.Send(ctx, actor, targetConversation.ID, forwardCommand)
	if err != nil || forwarded.Message.Type != "IMAGE" || len(forwarded.Message.Attachments) != 1 || forwarded.Message.Attachments[0].ID == result.ID {
		t.Fatal("IMAGE forward is not independent", forwarded, err)
	}
	var sourceObject, forwardedObject string
	if err = op.QueryRow(ctx, "SELECT object_id::text FROM chat.attachments WHERE project_id=$1 AND id=$2", project, result.ID).Scan(&sourceObject); err != nil {
		t.Fatal(err)
	}
	if err = op.QueryRow(ctx, "SELECT object_id::text FROM chat.attachments WHERE project_id=$1 AND id=$2", project, forwarded.Message.Attachments[0].ID).Scan(&forwardedObject); err != nil || sourceObject != forwardedObject {
		t.Fatal("IMAGE forward copied or lost object identity", err)
	}
	if _, err = messages.Delete(ctx, actor, sent.Message.ID, sourceConversation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Download(ctx, reader, result.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("deleted source attachment remains accessible", err)
	}
	if _, err = service.Download(ctx, reader, forwarded.Message.Attachments[0].ID); err != nil {
		t.Fatal("forward attachment depended on deleted source", err)
	}
	var key, objectStatus, attachmentStatus string
	var digest []byte
	if err = op.QueryRow(ctx, `SELECT o.storage_key,o.status,a.status,o.sha256 FROM chat.attachments a JOIN chat.storage_objects o ON o.project_id=a.project_id AND o.id=a.object_id WHERE a.project_id=$1 AND a.id=$2`, project, result.ID).Scan(&key, &objectStatus, &attachmentStatus, &digest); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "project/"+project+"/attachments/") || objectStatus != "ready" || attachmentStatus != "deleted" || len(digest) != 32 {
		t.Fatal("invalid persisted upload metadata")
	}
	object, err := clients.Storage.Client.GetObject(ctx, clients.Storage.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := io.ReadAll(object)
	object.Close()
	if err != nil || !bytes.Equal(stored, body) {
		t.Fatal("stored image mismatch", err)
	}
	if _, err = op.Exec(ctx, "UPDATE chat.project_settings SET flags=jsonb_set(flags,'{allow_images}','false') WHERE project_id=$1", project); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Limit(ctx, actor); !errors.Is(err, policy.ErrFeatureDisabled) {
		t.Fatal("allow_images bypass", err)
	}
	if _, err = op.Exec(ctx, "UPDATE chat.project_settings SET flags=jsonb_set(flags,'{allow_images}','true'),max_upload_size=1 WHERE project_id=$1", project); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Upload(ctx, actor, attachment.Input{Name: "large.png", DeclaredMIME: "image/png", Size: int64(len(body)), Reader: bytes.NewReader(body)}); !errors.Is(err, attachment.ErrTooLarge) {
		t.Fatal("project upload bound bypass", err)
	}
	if _, err = op.Exec(ctx, "UPDATE chat.project_settings SET max_upload_size=10485760 WHERE project_id=$1", project); err != nil {
		t.Fatal(err)
	}
	broken := attachment.New(repo, failingBlobs{}, cfg)
	if _, err = broken.Upload(ctx, actor, attachment.Input{Name: "failed.png", DeclaredMIME: "image/png", Size: int64(len(body)), Reader: bytes.NewReader(body)}); !errors.Is(err, attachment.ErrStorage) {
		t.Fatal("storage failure not surfaced", err)
	}
	var uploading int
	if err = op.QueryRow(ctx, "SELECT count(*) FROM chat.attachments WHERE project_id=$1 AND status='uploading'", project).Scan(&uploading); err != nil || uploading != 1 {
		t.Fatal("failed upload lost recoverable lifecycle row", uploading, err)
	}
	if _, err = op.Exec(ctx, "UPDATE chat.storage_objects SET created_at=clock_timestamp()-interval '2 hours' WHERE project_id=$1 AND status='uploading'", project); err != nil {
		t.Fatal(err)
	}
	cleaner := &attachmentrepo.Cleaner{DB: clients.Postgres, Blobs: clients.Storage}
	if worked, e := cleaner.One(ctx); e != nil || !worked {
		t.Fatal("stale uploading object was not reconciled", worked, e)
	}
	var deletedObjects int
	if err = op.QueryRow(ctx, "SELECT count(*) FROM chat.storage_objects WHERE project_id=$1 AND status='deleted'", project).Scan(&deletedObjects); err != nil || deletedObjects != 1 {
		t.Fatal("cleanup lifecycle not durable", deletedObjects, err)
	}
	if _, err = op.Exec(ctx, "UPDATE chat.storage_objects SET created_at=clock_timestamp()-interval '2 hours' WHERE project_id=$1 AND id=$2", project, sourceObject); err != nil {
		t.Fatal(err)
	}
	if worked, e := cleaner.One(ctx); e != nil || worked {
		t.Fatal("cleaner selected object still referenced by forward", worked, e)
	}
	// Runtime credentials cannot rewrite immutable ownership or erase evidence.
	if _, err = clients.Postgres.Exec(ctx, "UPDATE chat.attachments SET uploader_id=$3 WHERE project_id=$1 AND id=$2", project, result.ID, uuid.NewString()); err == nil {
		t.Fatal("runtime rewrote attachment owner")
	}
	if _, err = clients.Postgres.Exec(ctx, "DELETE FROM chat.attachments WHERE project_id=$1 AND id=$2", project, result.ID); err == nil {
		t.Fatal("runtime deleted attachment")
	}
}
