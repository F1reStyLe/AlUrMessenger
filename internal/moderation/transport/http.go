// Package transport exposes Project-admin blacklist management.
package transport

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"

	identityhttp "github.com/F1reStyLe/AlUrMessenger/internal/identity/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/moderation"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/admission"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/httpserver"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/google/uuid"
)

func failure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, policy.ErrInvalid):
		httpserver.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid blacklist request")
	case errors.Is(err, policy.ErrConflict):
		httpserver.WriteError(w, r, 409, "VERSION_CONFLICT", "Blacklist word already exists or version is stale")
	case errors.Is(err, policy.ErrForbidden):
		httpserver.WriteError(w, r, 403, "FORBIDDEN", "Project admin role required")
	default:
		identityhttp.Failure(w, r, err)
	}
}

func reportFailure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, policy.ErrInvalid):
		httpserver.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid report request")
	case errors.Is(err, policy.ErrConflict):
		httpserver.WriteError(w, r, 409, "VERSION_CONFLICT", "Report status transition or version is stale")
	case errors.Is(err, policy.ErrForbidden):
		httpserver.WriteError(w, r, 403, "FORBIDDEN", "Report operation is not permitted")
	default:
		identityhttp.Failure(w, r, err)
	}
}

// reportPagination accepts the shared opaque UUID cursor plus one exact status
// filter. Duplicate/unknown query parameters fail rather than being ignored.
func reportPagination(w http.ResponseWriter, r *http.Request) (status, after string, limit int, ok bool) {
	q := r.URL.Query()
	limit, ok = 50, true
	for key, values := range q {
		if (key != "cursor" && key != "limit" && key != "status") || len(values) != 1 {
			ok = false
		}
	}
	status = q.Get("status")
	if q.Has("limit") {
		var err error
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			ok = false
		}
	}
	if q.Has("cursor") {
		decoded, err := base64.RawURLEncoding.DecodeString(q.Get("cursor"))
		id, parseErr := uuid.Parse(string(decoded))
		if err != nil || parseErr != nil || len(decoded) != 36 {
			ok = false
		} else {
			after = id.String()
		}
	}
	if !ok {
		httpserver.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid report filters or pagination")
	}
	return
}

func trace(r *http.Request) policy.Trace {
	return policy.Trace{RequestID: httpserver.RequestID(r.Context()), IP: admission.PeerIP(r)}
}

func Register(server *httpserver.Server, service *moderation.Service, protect func(http.Handler) http.Handler) {
	server.Handle("/admin/v1/blacklist", protect(identityhttp.Methods("GET, POST", func(w http.ResponseWriter, r *http.Request) {
		actor := identityhttp.Actor(r.Context())
		if r.Method == "POST" {
			var command moderation.Create
			if !httpserver.ReadPatchJSON(w, r, &command) {
				return
			}
			result, err := service.Create(r.Context(), actor, command, trace(r))
			if err != nil {
				failure(w, r, err)
				return
			}
			w.Header().Set("Location", "/admin/v1/blacklist/"+result.ID)
			httpserver.WriteJSON(w, r, 201, result)
			return
		}
		after, limit, ok := identityhttp.Pagination(w, r)
		if !ok {
			return
		}
		items, err := service.List(r.Context(), actor, after, limit+1)
		if err != nil {
			failure(w, r, err)
			return
		}
		var next *string
		if len(items) > limit {
			items = items[:limit]
			cursor := identityhttp.Cursor(items[len(items)-1].ID)
			next = &cursor
		}
		httpserver.WriteJSON(w, r, 200, struct {
			Items []moderation.Entry `json:"items"`
			Next  *string            `json:"next_cursor"`
		}{items, next})
	})))
	server.Handle("/admin/v1/blacklist/{id}", protect(identityhttp.Methods("PATCH, DELETE", func(w http.ResponseWriter, r *http.Request) {
		actor, id := identityhttp.Actor(r.Context()), r.PathValue("id")
		if r.Method == "DELETE" {
			if err := service.Delete(r.Context(), actor, id, trace(r)); err != nil {
				failure(w, r, err)
				return
			}
			w.WriteHeader(204)
			return
		}
		var patch moderation.Patch
		if !httpserver.ReadPatchJSON(w, r, &patch) {
			return
		}
		result, err := service.Update(r.Context(), actor, id, patch, trace(r))
		if err != nil {
			failure(w, r, err)
			return
		}
		httpserver.WriteJSON(w, r, 200, result)
	})))
}

// RegisterReports keeps reporter-safe and reviewer DTOs on separate endpoints:
// ordinary users never receive reviewer identity or timestamps.
func RegisterReports(server *httpserver.Server, service *moderation.ReportService, protect func(http.Handler) http.Handler) {
	create := func(user bool) http.Handler {
		return protect(identityhttp.Methods("POST", func(w http.ResponseWriter, r *http.Request) {
			var command moderation.ReportCreate
			if !httpserver.ReadJSON(w, r, &command) {
				return
			}
			actor, id := identityhttp.Actor(r.Context()), r.PathValue("id")
			var result moderation.Report
			var err error
			if user {
				result, err = service.CreateUser(r.Context(), actor, id, command)
			} else {
				result, err = service.CreateMessage(r.Context(), actor, id, command)
			}
			if err != nil {
				reportFailure(w, r, err)
				return
			}
			w.Header().Set("Location", "/api/v1/reports/"+result.ID)
			httpserver.WriteJSON(w, r, 201, result)
		}))
	}
	server.Handle("/api/v1/messages/{id}/reports", create(false))
	server.Handle("/api/v1/users/{id}/reports", create(true))
	server.Handle("/api/v1/reports/{id}", protect(identityhttp.Methods("GET", func(w http.ResponseWriter, r *http.Request) {
		result, err := service.GetOwn(r.Context(), identityhttp.Actor(r.Context()), r.PathValue("id"))
		if err != nil {
			reportFailure(w, r, err)
			return
		}
		httpserver.WriteJSON(w, r, 200, result)
	})))
	server.Handle("/admin/v1/reports", protect(identityhttp.Methods("GET", func(w http.ResponseWriter, r *http.Request) {
		status, after, limit, ok := reportPagination(w, r)
		if !ok {
			return
		}
		items, err := service.List(r.Context(), identityhttp.Actor(r.Context()), status, after, limit+1)
		if err != nil {
			reportFailure(w, r, err)
			return
		}
		var next *string
		if len(items) > limit {
			items = items[:limit]
			cursor := identityhttp.Cursor(items[len(items)-1].ID)
			next = &cursor
		}
		httpserver.WriteJSON(w, r, 200, struct {
			Items []moderation.ReviewedReport `json:"items"`
			Next  *string                     `json:"next_cursor"`
		}{items, next})
	})))
	server.Handle("/admin/v1/reports/{id}", protect(identityhttp.Methods("GET, PATCH", func(w http.ResponseWriter, r *http.Request) {
		actor, id := identityhttp.Actor(r.Context()), r.PathValue("id")
		if r.Method == "GET" {
			result, err := service.GetAdmin(r.Context(), actor, id)
			if err != nil {
				reportFailure(w, r, err)
				return
			}
			httpserver.WriteJSON(w, r, 200, result)
			return
		}
		var command moderation.Review
		if !httpserver.ReadPatchJSON(w, r, &command) {
			return
		}
		result, err := service.Review(r.Context(), actor, id, command, trace(r))
		if err != nil {
			reportFailure(w, r, err)
			return
		}
		httpserver.WriteJSON(w, r, 200, result)
	})))
}
