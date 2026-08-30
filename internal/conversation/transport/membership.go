package transport

import (
	"net/http"

	"github.com/F1reStyLe/AlUrMessenger/internal/conversation"
	identityhttp "github.com/F1reStyLe/AlUrMessenger/internal/identity/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/admission"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/httpserver"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
)

// RegisterMembership uses authenticated actors only; the path's target user never
// becomes the actor. Every command is authorized again inside its SQL transaction.
func RegisterMembership(server *httpserver.Server, s *conversation.MembershipService, protect func(http.Handler) http.Handler) {
	server.Handle("/api/v1/conversations/{id}/members", protect(identityhttp.Methods("GET, POST", func(w http.ResponseWriter, r *http.Request) {
		a := identityhttp.Actor(r.Context())
		id := r.PathValue("id")
		if r.Method == "POST" {
			var p conversation.AddMembers
			if !httpserver.ReadPatchJSON(w, r, &p) {
				return
			}
			items, err := s.Add(r.Context(), a, id, p, policy.Trace{RequestID: httpserver.RequestID(r.Context()), IP: admission.PeerIP(r)})
			if err != nil {
				failure(w, r, err)
				return
			}
			httpserver.WriteJSON(w, r, 200, struct {
				Items []conversation.Member `json:"items"`
			}{items})
			return
		}
		after, limit, err := pagination(r, a, id)
		if err != nil {
			failure(w, r, err)
			return
		}
		items, err := s.Members(r.Context(), a, id, after, limit+1)
		if err != nil {
			failure(w, r, err)
			return
		}
		var next *string
		if len(items) > limit {
			items = items[:limit]
			cursor := cursorFor(a, items[len(items)-1].UserID, id)
			next = &cursor
		}
		httpserver.WriteJSON(w, r, 200, struct {
			Items []conversation.Member `json:"items"`
			Next  *string               `json:"next_cursor"`
		}{items, next})
	})))
	server.Handle("/api/v1/conversations/{id}/members/{user_id}", protect(identityhttp.Methods("PATCH, DELETE", func(w http.ResponseWriter, r *http.Request) {
		a := identityhttp.Actor(r.Context())
		id, user := r.PathValue("id"), r.PathValue("user_id")
		trace := policy.Trace{RequestID: httpserver.RequestID(r.Context()), IP: admission.PeerIP(r)}
		if r.Method == "DELETE" {
			if err := s.Remove(r.Context(), a, id, user, trace); err != nil {
				failure(w, r, err)
				return
			}
			w.WriteHeader(204)
			return
		}
		var p conversation.MemberPatch
		if !httpserver.ReadPatchJSON(w, r, &p) {
			return
		}
		m, err := s.Patch(r.Context(), a, id, user, p, trace)
		if err != nil {
			failure(w, r, err)
			return
		}
		httpserver.WriteJSON(w, r, 200, m)
	})))
}
