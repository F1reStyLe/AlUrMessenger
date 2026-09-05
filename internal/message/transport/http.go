// Package transport exposes message send/history/search without accepting actor identity.
package transport

import (
	"errors"
	"net/http"
	"slices"
	"strconv"

	identityhttp "github.com/F1reStyLe/AlUrMessenger/internal/identity/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/message"
	"github.com/F1reStyLe/AlUrMessenger/internal/moderation"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/httpserver"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
)

// Failure is shared by REST and future WS adapters through stable domain error codes.
func Failure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, message.ErrResync):
		httpserver.WriteError(w, r, 409, "RESYNC_REQUIRED", "Reload conversation snapshot before replay")
	case errors.Is(err, policy.ErrConflict):
		httpserver.WriteError(w, r, 409, "VERSION_CONFLICT", "Reload the current message version before editing")
	case errors.Is(err, policy.ErrInvalid):
		httpserver.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid message request")
	case errors.Is(err, policy.ErrForbidden):
		httpserver.WriteError(w, r, 403, "FORBIDDEN", "Conversation write permission required")
	case errors.Is(err, policy.ErrFeatureDisabled):
		httpserver.WriteError(w, r, 403, "FEATURE_DISABLED", "Project feature disabled")
	case errors.Is(err, message.ErrIdempotencyConflict):
		httpserver.WriteError(w, r, 409, "IDEMPOTENCY_CONFLICT", "Client message ID was used for a different command")
	case errors.Is(err, moderation.ErrContentRejected):
		httpserver.WriteError(w, r, 422, "CONTENT_REJECTED", "Content violates Project policy")
	default:
		identityhttp.Failure(w, r, err)
	}
}

// query rejects ambiguous bounds and repeated parameters, including an explicit
// after_sequence=0 which means forward traversal from the first message.
func query(r *http.Request, search bool) (message.Query, error) {
	values := r.URL.Query()
	q := message.Query{Limit: 50}
	for name, list := range values {
		if len(list) != 1 || (name != "limit" && name != "before_sequence" && name != "after_sequence" && !(search && name == "q")) {
			return q, policy.ErrInvalid
		}
	}
	if values.Has("before_sequence") && values.Has("after_sequence") {
		return q, policy.ErrInvalid
	}
	for name, target := range map[string]*int64{"before_sequence": &q.Before, "after_sequence": &q.After} {
		if values.Has(name) {
			v, err := strconv.ParseInt(values.Get(name), 10, 64)
			if err != nil || v < 0 || (name == "before_sequence" && v == 0) {
				return q, policy.ErrInvalid
			}
			*target = v
		}
	}
	q.Forward = values.Has("after_sequence")
	if search && q.Forward {
		return q, policy.ErrInvalid
	}
	if values.Has("limit") {
		v, err := strconv.Atoi(values.Get("limit"))
		if err != nil || v < 1 || v > 100 {
			return q, policy.ErrInvalid
		}
		q.Limit = v
	}
	return q, nil
}

// page trims lookahead before reordering history into ASC; search stays DESC by contract.
func page(w http.ResponseWriter, r *http.Request, items []message.Message, q message.Query, search bool) {
	more := len(items) > q.Limit
	var next *string
	if more {
		items = items[:q.Limit]
		cursor := strconv.FormatInt(items[len(items)-1].Sequence, 10)
		next = &cursor
	}
	if !search && !q.Forward && q.After == 0 {
		slices.Reverse(items)
	}
	httpserver.WriteJSON(w, r, 200, struct {
		Items []message.Message `json:"items"`
		Next  *string           `json:"next_cursor"`
		More  bool              `json:"has_more"`
	}{items, next, more})
}

// Register binds one shared application service to the standard auth/admission chain.
func Register(server *httpserver.Server, s *message.Service, protect func(http.Handler) http.Handler) {
	// Relation commands never take user_id/pinned_by from clients. The service
	// receives only the verified actor and the exact route resource scope.
	server.Handle("/api/v1/messages/{id}/reactions/{reaction}", protect(identityhttp.Methods("PUT, DELETE", func(w http.ResponseWriter, r *http.Request) {
		m, err := s.React(r.Context(), identityhttp.Actor(r.Context()), r.PathValue("id"), "", r.PathValue("reaction"), r.Method == "PUT")
		if err != nil {
			Failure(w, r, err)
			return
		}
		httpserver.WriteJSON(w, r, 200, m)
	})))
	server.Handle("/api/v1/conversations/{id}/pins", protect(identityhttp.Methods("GET", func(w http.ResponseWriter, r *http.Request) {
		p, err := s.Pins(r.Context(), identityhttp.Actor(r.Context()), r.PathValue("id"))
		if err != nil {
			Failure(w, r, err)
			return
		}
		httpserver.WriteJSON(w, r, 200, p)
	})))
	server.Handle("/api/v1/conversations/{id}/pins/{message_id}", protect(identityhttp.Methods("PUT, DELETE", func(w http.ResponseWriter, r *http.Request) {
		m, err := s.SetPin(r.Context(), identityhttp.Actor(r.Context()), r.PathValue("id"), r.PathValue("message_id"), r.Method == "PUT")
		if err != nil {
			Failure(w, r, err)
			return
		}
		if r.Method == "DELETE" {
			w.WriteHeader(204)
			return
		}
		httpserver.WriteJSON(w, r, 200, m.Pin)
	})))
	server.Handle("/api/v1/conversations/{id}/messages", protect(identityhttp.Methods("GET, POST", func(w http.ResponseWriter, r *http.Request) {
		a := identityhttp.Actor(r.Context())
		id := r.PathValue("id")
		if r.Method == "POST" {
			var p message.Send
			if !httpserver.ReadPatchJSON(w, r, &p) {
				return
			}
			sent, err := s.Send(r.Context(), a, id, p)
			if err != nil {
				Failure(w, r, err)
				return
			}
			status := 201
			if sent.Deduplicated {
				status = 200
			} else {
				w.Header().Set("Location", "/api/v1/messages/"+sent.Message.ID)
			}
			httpserver.WriteJSON(w, r, status, sent)
			return
		}
		q, err := query(r, false)
		if err != nil {
			Failure(w, r, err)
			return
		}
		fetch := q
		fetch.Limit++
		items, err := s.History(r.Context(), a, id, fetch)
		if err != nil {
			Failure(w, r, err)
			return
		}
		page(w, r, items, q, false)
	})))
	server.Handle("/api/v1/messages/{id}", protect(identityhttp.Methods("GET, PATCH, DELETE", func(w http.ResponseWriter, r *http.Request) {
		a, id := identityhttp.Actor(r.Context()), r.PathValue("id")
		var m message.Message
		var err error
		switch r.Method {
		case "PATCH":
			var p message.Edit
			if !httpserver.ReadPatchJSON(w, r, &p) {
				return
			}
			m, err = s.Edit(r.Context(), a, id, "", p)
		case "DELETE":
			m, err = s.Delete(r.Context(), a, id, "")
		default:
			m, err = s.Get(r.Context(), a, id)
		}
		if err != nil {
			Failure(w, r, err)
			return
		}
		httpserver.WriteJSON(w, r, 200, m)
	})))
	server.Handle("/api/v1/conversations/{id}/search", protect(identityhttp.Methods("GET", func(w http.ResponseWriter, r *http.Request) {
		q, err := query(r, true)
		if err != nil {
			Failure(w, r, err)
			return
		}
		fetch := q
		fetch.Limit++
		items, err := s.Search(r.Context(), identityhttp.Actor(r.Context()), r.PathValue("id"), r.URL.Query().Get("q"), fetch)
		if err != nil {
			Failure(w, r, err)
			return
		}
		page(w, r, items, q, true)
	})))
}
