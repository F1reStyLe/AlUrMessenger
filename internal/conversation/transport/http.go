// Package transport exposes only implemented conversation endpoints with shared Auth/CORS/limits.
package transport

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/F1reStyLe/AlUrMessenger/internal/conversation"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	identityhttp "github.com/F1reStyLe/AlUrMessenger/internal/identity/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/admission"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/httpserver"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
)

// failure never reveals a private conversation through a SQL/driver error.
func failure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, policy.ErrInvalid):
		httpserver.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid conversation request")
	case errors.Is(err, policy.ErrForbidden):
		httpserver.WriteError(w, r, 403, "FORBIDDEN", "Conversation permission required")
	case errors.Is(err, policy.ErrFeatureDisabled):
		httpserver.WriteError(w, r, 403, "FEATURE_DISABLED", "Project feature disabled")
	case errors.Is(err, policy.ErrConflict):
		httpserver.WriteError(w, r, 409, "VERSION_CONFLICT", "Reload conversation and retry with current version")
	case errors.Is(err, conversation.ErrUnsupported):
		httpserver.WriteError(w, r, 422, "CONVERSATION_OPERATION_UNSUPPORTED", "Operation is not supported for DIRECT")
	case errors.Is(err, conversation.ErrLastModerator):
		httpserver.WriteError(w, r, 409, "LAST_MODERATOR_REQUIRED", "Assign another active moderator first")
	default:
		identityhttp.Failure(w, r, err)
	}
}

// listCursor is opaque pagination state scoped to the current actor and Project.
// It is not a credential: SQL membership remains mandatory even for a forged cursor.
type listCursor struct {
	Resource string `json:"r,omitempty"`
	Project  string `json:"p"`
	User     string `json:"u"`
	After    string `json:"a"`
}

// pagination rejects repeated/unknown parameters, cross-actor cursors and unbounded work.
func pagination(r *http.Request, a identity.Actor, resource string) (string, int, error) {
	query := r.URL.Query()
	limit := 50
	for k, values := range query {
		if (k != "cursor" && k != "limit") || len(values) != 1 {
			return "", 0, policy.ErrInvalid
		}
	}
	if query.Has("limit") {
		v, err := strconv.Atoi(query.Get("limit"))
		if err != nil || v < 1 || v > 100 {
			return "", 0, policy.ErrInvalid
		}
		limit = v
	}
	if !query.Has("cursor") {
		return "", limit, nil
	}
	if len(query.Get("cursor")) > 512 {
		return "", 0, policy.ErrInvalid
	}
	data, err := base64.RawURLEncoding.DecodeString(query.Get("cursor"))
	var cursor listCursor
	if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Resource != resource || cursor.Project != a.ProjectID || cursor.User != a.User.ID || !conversation.ValidID(cursor.After) {
		return "", 0, policy.ErrInvalid
	}
	return cursor.After, limit, nil
}

// cursorFor never includes a bearer token or an external user identity.
func cursorFor(a identity.Actor, id, resource string) string {
	data, _ := json.Marshal(listCursor{Project: a.ProjectID, User: a.User.ID, After: id, Resource: resource})
	return base64.RawURLEncoding.EncodeToString(data)
}

// Register requires the same admission chain as existing identity/policy endpoints.
func Register(server *httpserver.Server, s *conversation.Service, protect func(http.Handler) http.Handler) {
	server.Handle("/api/v1/conversations", protect(identityhttp.Methods("GET, POST", func(w http.ResponseWriter, r *http.Request) {
		a := identityhttp.Actor(r.Context())
		if r.Method == http.MethodPost {
			var p conversation.Create
			// Creation also has non-nullable fields; use the strict object decoder.
			if !httpserver.ReadPatchJSON(w, r, &p) {
				return
			}
			c, created, err := s.Create(r.Context(), a, p, policy.Trace{RequestID: httpserver.RequestID(r.Context()), IP: admission.PeerIP(r)})
			if err != nil {
				failure(w, r, err)
				return
			}
			status := 200
			if created {
				status = 201
				w.Header().Set("Location", "/api/v1/conversations/"+c.ID)
			}
			httpserver.WriteJSON(w, r, status, c)
			return
		}
		after, limit, err := pagination(r, a, "")
		if err != nil {
			failure(w, r, err)
			return
		}
		items, err := s.List(r.Context(), a, after, limit+1)
		if err != nil {
			failure(w, r, err)
			return
		}
		var next *string
		if len(items) > limit {
			items = items[:limit]
			cursor := cursorFor(a, items[len(items)-1].ID, "")
			next = &cursor
		}
		httpserver.WriteJSON(w, r, 200, struct {
			Items []conversation.Conversation `json:"items"`
			Next  *string                     `json:"next_cursor"`
		}{items, next})
	})))
	server.Handle("/api/v1/conversations/{id}", protect(identityhttp.Methods("GET, PATCH", func(w http.ResponseWriter, r *http.Request) {
		a := identityhttp.Actor(r.Context())
		id := r.PathValue("id")
		var c conversation.Conversation
		var err error
		if r.Method == http.MethodGet {
			c, err = s.Get(r.Context(), a, id)
		} else {
			var p conversation.Patch
			if !httpserver.ReadPatchJSON(w, r, &p) {
				return
			}
			c, err = s.Patch(r.Context(), a, id, p, policy.Trace{RequestID: httpserver.RequestID(r.Context()), IP: admission.PeerIP(r)})
		}
		if err != nil {
			failure(w, r, err)
			return
		}
		httpserver.WriteJSON(w, r, 200, c)
	})))
}
