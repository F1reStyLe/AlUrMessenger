// Package transport exposes Project-admin blacklist management.
package transport

import (
	"errors"
	"net/http"

	identityhttp "github.com/F1reStyLe/AlUrMessenger/internal/identity/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/moderation"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/admission"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/httpserver"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
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
