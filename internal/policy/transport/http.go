// Package transport exposes the implemented Project administration subset.
package transport

import (
	"errors"
	identityhttp "github.com/F1reStyLe/AlUrMessenger/internal/identity/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/admission"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/httpserver"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"net/http"
)

// failure maps only closed domain errors; SQL errors are handled by the shared safe mapper.
func failure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, policy.ErrForbidden):
		httpserver.WriteError(w, r, 403, "FORBIDDEN", "Project admin role required")
	case errors.Is(err, policy.ErrConflict):
		httpserver.WriteError(w, r, 409, "VERSION_CONFLICT", "Reload settings and retry with current version")
	case errors.Is(err, policy.ErrInvalid):
		httpserver.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid settings patch")
	default:
		identityhttp.Failure(w, r, err)
	}
}

// Register requires the same auth/admission chain as identity routes; no anonymous admin API.
func Register(server *httpserver.Server, service *policy.Service, protect func(http.Handler) http.Handler) {
	for _, path := range []string{"/admin/v1/project", "/admin/v1/feature-flags"} {
		server.Handle(path, protect(identityhttp.Methods("GET, PATCH", func(w http.ResponseWriter, r *http.Request) {
			a := identityhttp.Actor(r.Context())
			if !a.Admin {
				failure(w, r, policy.ErrForbidden)
				return
			}
			var settings policy.Settings
			var err error
			if r.Method == "GET" {
				settings, err = service.Get(r.Context(), a)
			} else {
				var patch policy.Patch
				if !httpserver.ReadJSON(w, r, &patch) {
					return
				}
				// Flags endpoint cannot mutate numeric settings through an alias.
				if r.URL.Path == "/admin/v1/feature-flags" && (patch.MaxUploadSize != nil || patch.RetentionDays != nil) {
					failure(w, r, policy.ErrInvalid)
					return
				}
				settings, err = service.Update(r.Context(), a, patch, policy.Trace{RequestID: httpserver.RequestID(r.Context()), IP: admission.PeerIP(r)})
			}
			if err != nil {
				failure(w, r, err)
				return
			}
			httpserver.WriteJSON(w, r, 200, settings)
		})))
	}
	server.Handle("/admin/v1/audit-logs", protect(identityhttp.Methods("GET", func(w http.ResponseWriter, r *http.Request) {
		a := identityhttp.Actor(r.Context())
		if !a.Admin {
			failure(w, r, policy.ErrForbidden)
			return
		}
		after, limit, ok := identityhttp.Pagination(w, r)
		if !ok {
			return
		}
		items, err := service.Audit(r.Context(), a, after, limit+1)
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
			Items []policy.Audit `json:"items"`
			Next  *string        `json:"next_cursor"`
		}{items, next})
	})))
}
