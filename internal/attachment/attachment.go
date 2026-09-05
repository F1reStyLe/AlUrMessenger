// Package attachment owns validated image uploads independently of HTTP and MinIO SDKs.
package attachment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/google/uuid"
	_ "golang.org/x/image/webp"
)

var (
	ErrTooLarge     = errors.New("ATTACHMENT_TOO_LARGE")
	ErrUnsupported  = errors.New("ATTACHMENT_MEDIA_UNSUPPORTED")
	ErrInvalidImage = errors.New("ATTACHMENT_INVALID_IMAGE")
	ErrStorage      = errors.New("ATTACHMENT_STORAGE_UNAVAILABLE")
	ErrLifecycle    = errors.New("ATTACHMENT_LIFECYCLE_CONFLICT")
)

// Attachment contains safe metadata only. Storage keys and hashes never leave the service.
type Attachment struct {
	ID           string    `json:"id"`
	OriginalName string    `json:"original_name"`
	MIME         string    `json:"mime_type"`
	Size         int64     `json:"size"`
	Width        int       `json:"width"`
	Height       int       `json:"height"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// Input is already isolated from multipart framing, but remains untrusted.
type Input struct {
	Name         string
	DeclaredMIME string
	Size         int64
	Reader       io.ReadSeeker
}

// Prepared binds immutable DB/object metadata after complete validation.
type Prepared struct {
	AttachmentID string
	ObjectID     string
	StorageKey   string
	Name         string
	MIME         string
	Size         int64
	Width        int
	Height       int
	SHA256       [32]byte
}

type Repository interface {
	Limit(context.Context, identity.Actor, int64) (int64, error)
	Begin(context.Context, identity.Actor, Prepared, int64) (Attachment, error)
	Ready(context.Context, identity.Actor, string) (Attachment, error)
}

type BlobStore interface {
	Put(context.Context, string, io.Reader, int64, string) error
}

// Service bounds expensive decode work across concurrent requests.
type Service struct {
	Repository Repository
	Blobs      BlobStore
	Config     config.Upload
	decode     chan struct{}
}

func New(repo Repository, blobs BlobStore, cfg config.Upload) *Service {
	return &Service{Repository: repo, Blobs: blobs, Config: cfg, decode: make(chan struct{}, cfg.DecodeConcurrency)}
}

// Limit authorizes the actor before the transport accepts a potentially large body.
func (s *Service) Limit(ctx context.Context, actor identity.Actor) (int64, error) {
	return s.Repository.Limit(ctx, actor, s.Config.MaxBytes)
}

// Upload fully validates and decodes the seekable image before creating lifecycle rows.
func (s *Service) Upload(ctx context.Context, actor identity.Actor, in Input) (Attachment, error) {
	limit, err := s.Limit(ctx, actor)
	if err != nil {
		return Attachment{}, err
	}
	if in.Reader == nil || in.Size < 1 || in.Size > limit {
		return Attachment{}, ErrTooLarge
	}
	name, extension, err := safeName(in.Name)
	if err != nil {
		return Attachment{}, err
	}
	declared, _, err := mime.ParseMediaType(in.DeclaredMIME)
	if err != nil {
		return Attachment{}, ErrUnsupported
	}
	if _, err = in.Reader.Seek(0, io.SeekStart); err != nil {
		return Attachment{}, ErrInvalidImage
	}
	header := make([]byte, min(in.Size, 512))
	if _, err = io.ReadFull(in.Reader, header); err != nil {
		return Attachment{}, ErrInvalidImage
	}
	actual := actualMIME(header)
	if actual == "" || declared != actual || !extensionMatches(extension, actual) {
		return Attachment{}, ErrUnsupported
	}
	if !exactContainerEnd(in.Reader, in.Size, actual) {
		return Attachment{}, ErrInvalidImage
	}
	if _, err = in.Reader.Seek(0, io.SeekStart); err != nil {
		return Attachment{}, ErrInvalidImage
	}
	cfg, format, err := image.DecodeConfig(in.Reader)
	if err != nil || formatMIME(format) != actual || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > s.Config.MaxDimension || cfg.Height > s.Config.MaxDimension || int64(cfg.Width)*int64(cfg.Height) > s.Config.MaxPixels {
		return Attachment{}, ErrInvalidImage
	}
	select {
	case s.decode <- struct{}{}:
		defer func() { <-s.decode }()
	case <-ctx.Done():
		return Attachment{}, ctx.Err()
	}
	if _, err = in.Reader.Seek(0, io.SeekStart); err != nil {
		return Attachment{}, ErrInvalidImage
	}
	if _, format, err = image.Decode(in.Reader); err != nil || formatMIME(format) != actual || ctx.Err() != nil {
		return Attachment{}, ErrInvalidImage
	}
	if _, err = in.Reader.Seek(0, io.SeekStart); err != nil {
		return Attachment{}, ErrInvalidImage
	}
	hash := sha256.New()
	if copied, e := io.Copy(hash, in.Reader); e != nil || copied != in.Size {
		return Attachment{}, ErrInvalidImage
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	attachmentID, err := uuid.NewV7()
	if err != nil {
		return Attachment{}, err
	}
	objectID, err := uuid.NewV7()
	if err != nil {
		return Attachment{}, err
	}
	p := Prepared{AttachmentID: attachmentID.String(), ObjectID: objectID.String(), StorageKey: "project/" + actor.ProjectID + "/attachments/" + objectID.String(), Name: name, MIME: actual, Size: in.Size, Width: cfg.Width, Height: cfg.Height, SHA256: digest}
	attachment, err := s.Repository.Begin(ctx, actor, p, s.Config.MaxBytes)
	if err != nil {
		return Attachment{}, err
	}
	if _, err = in.Reader.Seek(0, io.SeekStart); err != nil {
		return Attachment{}, ErrInvalidImage
	}
	if err = s.Blobs.Put(ctx, p.StorageKey, in.Reader, p.Size, p.MIME); err != nil {
		return Attachment{}, ErrStorage
	}
	attachment, err = s.Repository.Ready(ctx, actor, attachment.ID)
	if err != nil {
		// The uploading row and namespaced object are intentionally retained for
		// the durable reconciliation/sweeper introduced in step 6.3.
		return Attachment{}, err
	}
	return attachment, nil
}

func safeName(value string) (string, string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, `\`, "/"))
	value = path.Base(value)
	if value == "." || value == "" || !utf8.ValidString(value) || len(value) > 255 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", "", policy.ErrInvalid
	}
	return value, strings.ToLower(path.Ext(value)), nil
}

func actualMIME(header []byte) string {
	switch {
	case len(header) >= 8 && bytes.Equal(header[:8], []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case len(header) >= 3 && header[0] == 0xff && header[1] == 0xd8 && header[2] == 0xff:
		return "image/jpeg"
	case len(header) >= 12 && bytes.Equal(header[:4], []byte("RIFF")) && bytes.Equal(header[8:12], []byte("WEBP")):
		return "image/webp"
	default:
		return ""
	}
}

func formatMIME(format string) string {
	switch format {
	case "png":
		return "image/png"
	case "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return ""
	}
}

func extensionMatches(extension, media string) bool {
	return media == "image/png" && extension == ".png" || media == "image/webp" && extension == ".webp" || media == "image/jpeg" && (extension == ".jpg" || extension == ".jpeg")
}

// exactContainerEnd rejects executable/polyglot trailers that permissive image
// decoders commonly ignore. Full decode separately checks compressed image integrity.
func exactContainerEnd(reader io.ReadSeeker, size int64, media string) bool {
	switch media {
	case "image/jpeg":
		if size < 2 {
			return false
		}
		buf := make([]byte, 2)
		_, err := reader.Seek(-2, io.SeekEnd)
		_, readErr := io.ReadFull(reader, buf)
		return err == nil && readErr == nil && bytes.Equal(buf, []byte{0xff, 0xd9})
	case "image/webp":
		if size < 12 {
			return false
		}
		buf := make([]byte, 8)
		_, err := reader.Seek(0, io.SeekStart)
		_, readErr := io.ReadFull(reader, buf)
		return err == nil && readErr == nil && int64(binary.LittleEndian.Uint32(buf[4:8]))+8 == size
	case "image/png":
		if _, err := reader.Seek(8, io.SeekStart); err != nil {
			return false
		}
		position := int64(8)
		header := make([]byte, 8)
		for position+12 <= size {
			if _, err := io.ReadFull(reader, header); err != nil {
				return false
			}
			length := int64(binary.BigEndian.Uint32(header[:4]))
			position += 12 + length
			if length < 0 || position > size {
				return false
			}
			if _, err := reader.Seek(length+4, io.SeekCurrent); err != nil {
				return false
			}
			if bytes.Equal(header[4:8], []byte("IEND")) {
				return length == 0 && position == size
			}
		}
	}
	return false
}
