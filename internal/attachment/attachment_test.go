package attachment

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"testing"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
)

type memoryRepository struct {
	limit    int64
	prepared *Prepared
}

func (r *memoryRepository) Limit(context.Context, identity.Actor, int64) (int64, error) {
	return r.limit, nil
}
func (r *memoryRepository) Begin(_ context.Context, _ identity.Actor, p Prepared, _ int64) (Attachment, error) {
	r.prepared = &p
	return Attachment{ID: p.AttachmentID, OriginalName: p.Name, MIME: p.MIME, Size: p.Size, Width: p.Width, Height: p.Height, Status: "uploading", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (r *memoryRepository) Ready(_ context.Context, _ identity.Actor, id string) (Attachment, error) {
	p := r.prepared
	return Attachment{ID: id, OriginalName: p.Name, MIME: p.MIME, Size: p.Size, Width: p.Width, Height: p.Height, Status: "ready"}, nil
}
func (r *memoryRepository) Get(_ context.Context, _ identity.Actor, id string) (Authorized, error) {
	p := r.prepared
	return Authorized{Attachment: Attachment{ID: id, OriginalName: p.Name, Status: "ready"}, StorageKey: p.StorageKey}, nil
}

type memoryBlobs struct{ data []byte }

func (b *memoryBlobs) Put(_ context.Context, key string, body io.Reader, size int64, media string) error {
	b.data, _ = io.ReadAll(body)
	if int64(len(b.data)) != size || key == "" || media != "image/png" {
		return ErrStorage
	}
	return nil
}
func (b *memoryBlobs) Presign(context.Context, string, string, time.Duration) (string, error) {
	return "https://objects.test/signed", nil
}

func validPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var body bytes.Buffer
	if err := png.Encode(&body, image.NewNRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func TestUploadValidatesAndStoresPrivateMetadata(t *testing.T) {
	body := validPNG(t, 2, 3)
	repo := &memoryRepository{limit: int64(len(body))}
	blobs := &memoryBlobs{}
	service := New(repo, blobs, config.Upload{MaxBytes: 1024, MaxDimension: 10, MaxPixels: 100, DecodeConcurrency: 1})
	actor := identity.Actor{ProjectID: "01900000-0000-4000-8000-000000000001", User: identity.User{ID: "01900000-0000-4000-8000-000000000002"}}
	result, err := service.Upload(t.Context(), actor, Input{Name: `C:\fakepath\photo.PNG`, DeclaredMIME: "image/png", Size: int64(len(body)), Reader: bytes.NewReader(body)})
	if err != nil || result.Status != "ready" || result.OriginalName != "photo.PNG" || result.Width != 2 || result.Height != 3 || !bytes.Equal(blobs.data, body) {
		t.Fatal(result, err)
	}
	if repo.prepared == nil || repo.prepared.StorageKey != "project/"+actor.ProjectID+"/attachments/"+repo.prepared.ObjectID || repo.prepared.SHA256 == [32]byte{} {
		t.Fatal("unsafe or incomplete prepared metadata")
	}
}

func TestUploadRejectsMismatchDamageAndBounds(t *testing.T) {
	body := validPNG(t, 2, 3)
	actor := identity.Actor{}
	tests := []struct {
		name, filename, media string
		body                  []byte
		cfg                   config.Upload
		want                  error
	}{
		{"mime", "a.png", "image/jpeg", body, config.Upload{MaxBytes: 1024, MaxDimension: 10, MaxPixels: 100, DecodeConcurrency: 1}, ErrUnsupported},
		{"extension", "a.jpg", "image/png", body, config.Upload{MaxBytes: 1024, MaxDimension: 10, MaxPixels: 100, DecodeConcurrency: 1}, ErrUnsupported},
		{"polyglot trailer", "a.png", "image/png", append(append([]byte{}, body...), []byte("<script>")...), config.Upload{MaxBytes: 2048, MaxDimension: 10, MaxPixels: 100, DecodeConcurrency: 1}, ErrInvalidImage},
		{"corrupt", "a.png", "image/png", body[:len(body)-5], config.Upload{MaxBytes: 1024, MaxDimension: 10, MaxPixels: 100, DecodeConcurrency: 1}, ErrInvalidImage},
		{"dimensions", "a.png", "image/png", body, config.Upload{MaxBytes: 1024, MaxDimension: 1, MaxPixels: 100, DecodeConcurrency: 1}, ErrInvalidImage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &memoryRepository{limit: int64(len(tc.body))}
			_, err := New(repo, &memoryBlobs{}, tc.cfg).Upload(t.Context(), actor, Input{Name: tc.filename, DeclaredMIME: tc.media, Size: int64(len(tc.body)), Reader: bytes.NewReader(tc.body)})
			if !errors.Is(err, tc.want) || repo.prepared != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAuthorizedMetadataAndOneMinuteDownload(t *testing.T) {
	id := "01900000-0000-4000-8000-000000000010"
	repo := &memoryRepository{prepared: &Prepared{Name: "safe.png", StorageKey: "project/p/attachments/o"}}
	service := New(repo, &memoryBlobs{}, config.Upload{DecodeConcurrency: 1})
	if _, err := service.Get(t.Context(), identity.Actor{}, "bad"); err == nil {
		t.Fatal("invalid attachment id accepted")
	}
	before := time.Now().UTC()
	download, err := service.Download(t.Context(), identity.Actor{}, id)
	if err != nil || download.URL != "https://objects.test/signed" || download.ExpiresAt.Before(before.Add(59*time.Second)) || download.ExpiresAt.After(before.Add(61*time.Second)) {
		t.Fatal(download, err)
	}
}
