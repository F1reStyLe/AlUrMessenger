//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"strings"
	"testing"

	"github.com/F1reStyLe/AlUrMessenger/internal/attachment"
	attachmentrepo "github.com/F1reStyLe/AlUrMessenger/internal/attachment/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
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
	var key, objectStatus, attachmentStatus string
	var digest []byte
	if err = op.QueryRow(ctx, `SELECT o.storage_key,o.status,a.status,o.sha256 FROM chat.attachments a JOIN chat.storage_objects o ON o.project_id=a.project_id AND o.id=a.object_id WHERE a.project_id=$1 AND a.id=$2`, project, result.ID).Scan(&key, &objectStatus, &attachmentStatus, &digest); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "project/"+project+"/attachments/") || objectStatus != "ready" || attachmentStatus != "ready" || len(digest) != 32 {
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
	// Runtime credentials cannot rewrite immutable ownership or erase evidence.
	if _, err = clients.Postgres.Exec(ctx, "UPDATE chat.attachments SET uploader_id=$3 WHERE project_id=$1 AND id=$2", project, result.ID, uuid.NewString()); err == nil {
		t.Fatal("runtime rewrote attachment owner")
	}
	if _, err = clients.Postgres.Exec(ctx, "DELETE FROM chat.attachments WHERE project_id=$1 AND id=$2", project, result.ID); err == nil {
		t.Fatal("runtime deleted attachment")
	}
}
