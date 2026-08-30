package transport

import (
	"net/http"
	"strconv"

	identityhttp "github.com/F1reStyLe/AlUrMessenger/internal/identity/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/message"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/httpserver"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
)

// RegisterRecovery exposes exactly the same recovery/checkpoint service as WebSocket.
func RegisterRecovery(server *httpserver.Server, s *message.RecoveryService, protect func(http.Handler) http.Handler) {
	server.Handle("/api/v1/conversations/{id}/events", protect(identityhttp.Methods("GET", func(w http.ResponseWriter, r *http.Request) {
		after := int64(0)
		limit := 50
		var err error
		for key, values := range r.URL.Query() {
			if len(values) != 1 || (key != "after_event_sequence" && key != "limit") {
				Failure(w, r, policy.ErrInvalid)
				return
			}
		}
		if r.URL.Query().Has("after_event_sequence") {
			after, err = strconv.ParseInt(r.URL.Query().Get("after_event_sequence"), 10, 64)
			if err != nil {
				Failure(w, r, policy.ErrInvalid)
				return
			}
		}
		if r.URL.Query().Has("limit") {
			limit, err = strconv.Atoi(r.URL.Query().Get("limit"))
			if err != nil {
				Failure(w, r, policy.ErrInvalid)
				return
			}
		}
		result, err := s.Replay(r.Context(), identityhttp.Actor(r.Context()), r.PathValue("id"), after, limit)
		if err != nil {
			Failure(w, r, err)
			return
		}
		httpserver.WriteJSON(w, r, 200, result)
	})))
	server.Handle("/api/v1/conversations/{id}/snapshot", protect(identityhttp.Methods("GET", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			Failure(w, r, policy.ErrInvalid)
			return
		}
		result, err := s.Snapshot(r.Context(), identityhttp.Actor(r.Context()), r.PathValue("id"))
		if err != nil {
			Failure(w, r, err)
			return
		}
		httpserver.WriteJSON(w, r, 200, result)
	})))
	for _, kind := range []string{"read", "delivered"} {
		server.Handle("/api/v1/conversations/{id}/"+kind, protect(identityhttp.Methods("POST", func(w http.ResponseWriter, r *http.Request) {
			var p struct {
				Sequence *int64 `json:"sequence,string"`
			}
			if !httpserver.ReadPatchJSON(w, r, &p) {
				return
			}
			if p.Sequence == nil {
				Failure(w, r, policy.ErrInvalid)
				return
			}
			result, err := s.Receipt(r.Context(), identityhttp.Actor(r.Context()), r.PathValue("id"), kind, *p.Sequence)
			if err != nil {
				Failure(w, r, err)
				return
			}
			httpserver.WriteJSON(w, r, 200, result)
		})))
	}
}
