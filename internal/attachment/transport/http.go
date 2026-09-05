// Package transport exposes strict streaming multipart attachment uploads.
package transport

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"os"

	"github.com/F1reStyLe/AlUrMessenger/internal/attachment"
	identityhttp "github.com/F1reStyLe/AlUrMessenger/internal/identity/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/httpserver"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
)

func failure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, attachment.ErrTooLarge):
		httpserver.WriteError(w, r, http.StatusRequestEntityTooLarge, "ATTACHMENT_TOO_LARGE", "Image exceeds the effective upload limit")
	case errors.Is(err, attachment.ErrUnsupported):
		httpserver.WriteError(w, r, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA", "Only matching JPEG, PNG or WebP files are accepted")
	case errors.Is(err, attachment.ErrInvalidImage), errors.Is(err, policy.ErrInvalid):
		httpserver.WriteError(w, r, http.StatusBadRequest, "INVALID_IMAGE", "Image content or multipart form is invalid")
	case errors.Is(err, attachment.ErrStorage):
		httpserver.WriteError(w, r, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Object storage is unavailable")
	case errors.Is(err, attachment.ErrLifecycle):
		httpserver.WriteError(w, r, http.StatusConflict, "ATTACHMENT_CONFLICT", "Attachment lifecycle changed")
	default:
		identityhttp.Failure(w, r, err)
	}
}

// Register accepts exactly one `file` part. It spools with the actor's effective
// limit so multipart framing is checked before any database/object mutation.
func Register(server *httpserver.Server, service *attachment.Service, protect func(http.Handler) http.Handler) {
	server.Handle("/api/v1/attachments", protect(identityhttp.Methods("POST", func(w http.ResponseWriter, r *http.Request) {
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "multipart/form-data" {
			httpserver.WriteError(w, r, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA", "Content-Type must be multipart/form-data")
			return
		}
		actor := identityhttp.Actor(r.Context())
		limit, err := service.Limit(r.Context(), actor)
		if err != nil {
			failure(w, r, err)
			return
		}
		reader, err := r.MultipartReader()
		if err != nil {
			failure(w, r, attachment.ErrInvalidImage)
			return
		}
		part, err := reader.NextPart()
		if err != nil || part.FormName() != "file" || part.FileName() == "" {
			failure(w, r, attachment.ErrInvalidImage)
			return
		}
		name, declared := part.FileName(), part.Header.Get("Content-Type")
		temporary, err := os.CreateTemp("", "alur-image-upload-*")
		if err != nil {
			httpserver.WriteError(w, r, 500, "INTERNAL_ERROR", "Unable to stage upload")
			return
		}
		temporaryPath := temporary.Name()
		defer os.Remove(temporaryPath)
		defer temporary.Close()
		written, copyErr := io.Copy(temporary, io.LimitReader(part, limit+1))
		part.Close()
		if copyErr != nil || written > limit {
			failure(w, r, attachment.ErrTooLarge)
			return
		}
		if extra, nextErr := reader.NextPart(); nextErr != io.EOF {
			if extra != nil {
				extra.Close()
			}
			failure(w, r, attachment.ErrInvalidImage)
			return
		}
		if _, err = temporary.Seek(0, io.SeekStart); err != nil {
			failure(w, r, attachment.ErrInvalidImage)
			return
		}
		result, err := service.Upload(r.Context(), actor, attachment.Input{Name: name, DeclaredMIME: declared, Size: written, Reader: temporary})
		if err != nil {
			failure(w, r, err)
			return
		}
		w.Header().Set("Location", "/api/v1/attachments/"+result.ID)
		httpserver.WriteJSON(w, r, http.StatusCreated, result)
	})))
	server.Handle("/api/v1/attachments/{id}", protect(identityhttp.Methods("GET", func(w http.ResponseWriter, r *http.Request) {
		result, err := service.Get(r.Context(), identityhttp.Actor(r.Context()), r.PathValue("id"))
		if err != nil {
			failure(w, r, err)
			return
		}
		httpserver.WriteJSON(w, r, http.StatusOK, result)
	})))
	server.Handle("/api/v1/attachments/{id}/download-url", protect(identityhttp.Methods("POST", func(w http.ResponseWriter, r *http.Request) {
		result, err := service.Download(r.Context(), identityhttp.Actor(r.Context()), r.PathValue("id"))
		if err != nil {
			failure(w, r, err)
			return
		}
		httpserver.WriteJSON(w, r, http.StatusOK, result)
	})))
}
