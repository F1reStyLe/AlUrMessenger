package realtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/internal/auth"
	"github.com/F1reStyLe/AlUrMessenger/internal/conversation"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/message"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/admission"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/httpserver"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Frame is shared by commands, replies and reference events; all cursor integers
// are decimal strings. The raw auth payload never reaches logging or the Hub.
type Frame struct {
	Type           string          `json:"type"`
	RequestID      string          `json:"request_id,omitempty"`
	EventID        string          `json:"event_id,omitempty"`
	ConversationID string          `json:"conversation_id,omitempty"`
	EventSequence  string          `json:"event_sequence,omitempty"`
	Sequence       string          `json:"sequence,omitempty"`
	Payload        json.RawMessage `json:"payload"`
	// Server-only delivery policy, never accepted from JSON clients.
	PresenceReply bool `json:"-"`
}
type closeRequest struct {
	code   websocket.StatusCode
	reason string
}

// Connection bounds both incoming commands and outgoing frames. Exactly one writer
// owns socket data writes; send queues are never closed by competing goroutines.
type Connection struct {
	socket   *websocket.Conn
	cancel   context.CancelFunc
	send     chan Frame
	incoming chan Frame
	wake     chan struct{}
	closing  chan closeRequest
}

func (c *Connection) stop(code websocket.StatusCode, reason string) {
	select {
	case c.closing <- closeRequest{code, reason}:
	default:
	}
}
func (c *Connection) enqueue(f Frame) bool {
	select {
	case c.send <- f:
		return true
	default:
		c.stop(1013, "SLOW_CONSUMER")
		return false
	}
}

// decode rejects unknown fields, trailing JSON, and number precision loss.
func decode(data []byte, target any) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		return policy.ErrInvalid
	}
	for _, value := range object {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return policy.ErrInvalid
		}
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(target); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return policy.ErrInvalid
	}
	return nil
}
func frame(kind, request, conversationID string, payload any) Frame {
	data, _ := json.Marshal(payload)
	return Frame{Type: kind, RequestID: request, ConversationID: conversationID, Payload: data}
}

// Gateway is immutable after route registration; shared services recheck DB policy.
type Gateway struct {
	Hub           *Hub
	Identity      *identity.Service
	Messages      *message.Service
	Recovery      *message.RecoveryService
	Conversations *conversation.Service
	Presence      *Presence
	Limiter       *admission.Limiter
	AllowNoOrigin bool
}

// ServeHTTP checks Origin exactly, forbids query credentials and requires chat.v1.
// Library origin checks are disabled only after our stricter scheme/host allowlist.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		httpserver.WriteError(w, r, 405, "METHOD_NOT_ALLOWED", "GET required")
		return
	}
	origin := r.Header.Get("Origin")
	if len(r.Header.Values("Origin")) > 1 || (origin == "" && !g.AllowNoOrigin) || (origin != "" && !g.Limiter.Config.Origins[origin]) {
		httpserver.WriteError(w, r, 403, "ORIGIN_FORBIDDEN", "Origin not allowed")
		return
	}
	if r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" {
		httpserver.WriteError(w, r, 400, "INVALID_REQUEST", "Use the first auth frame")
		return
	}
	found := false
	for _, value := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, protocol := range strings.Split(value, ",") {
			if strings.TrimSpace(protocol) == "chat.v1" {
				found = true
			}
		}
	}
	if !found {
		httpserver.WriteError(w, r, 400, "INVALID_REQUEST", "chat.v1 subprotocol required")
		return
	}
	socket, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"chat.v1"}, InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Connection{socket: socket, cancel: cancel, send: make(chan Frame, 64), incoming: make(chan Frame, 16), wake: make(chan struct{}, 1), closing: make(chan closeRequest, 1)}
	if !g.Hub.add(c) {
		socket.CloseNow()
		cancel()
		return
	}
	defer g.Hub.remove(c)
	defer cancel()
	defer socket.CloseNow()
	// Deadline includes the first frame and identity verification, limiting idle upgrades.
	deadline, stop := context.WithTimeout(ctx, 5*time.Second)
	socket.SetReadLimit(8192)
	typ, data, err := socket.Read(deadline)
	var f Frame
	var credentials struct {
		AccessToken string `json:"access_token"`
		DeviceID    string `json:"device_id"`
	}
	if err == nil && (typ != websocket.MessageText || decode(data, &f) != nil || f.Type != "auth" || !conversation.ValidID(f.RequestID) || f.ConversationID != "" || decode(f.Payload, &credentials) != nil || !conversation.ValidID(credentials.DeviceID)) {
		err = auth.ErrUnauthenticated
	}
	var actor identity.Actor
	if err == nil {
		actor, err = g.Identity.Authenticate(deadline, credentials.AccessToken)
	}
	expires := time.Time{}
	if err == nil {
		// Auth has already verified this exact JWT. This parse reads expiration only;
		// Project, user and permissions never come from unverified payload claims.
		claims := &jwt.RegisteredClaims{}
		_, _, err = jwt.NewParser().ParseUnverified(credentials.AccessToken, claims)
		if err == nil && claims.ExpiresAt != nil {
			expires = claims.ExpiresAt.Time
		} else {
			err = auth.ErrUnauthenticated
		}
		if !expires.After(time.Now()) {
			err = auth.ErrUnauthenticated
		}
	}
	stop()
	if err != nil {
		code := websocket.StatusCode(4401)
		if errors.Is(err, auth.ErrUnavailable) {
			code = 1013
		}
		socket.Close(code, "AUTHENTICATION_FAILED")
		return
	}
	session := &Session{gateway: g, connection: c, actor: actor, token: credentials.AccessToken, device: credentials.DeviceID, id: uuid.NewString(), expires: expires, subscriptions: map[string]int64{}, presenceWatch: map[string][]string{}, presenceState: map[string]string{}, typingState: map[string]string{}}
	if err = g.Presence.Touch(ctx, actor, session.id, session.device); err != nil {
		socket.Close(1013, "PRESENCE_UNAVAILABLE")
		return
	}
	defer g.Presence.Drop(context.Background(), actor, session.id)
	g.Hub.identify(c, actor.ProjectID)
	socket.SetReadLimit(131072)
	session.run(ctx, f.RequestID)
}

// errorCode deliberately discards driver/upstream errors instead of exposing internals.
func errorCode(err error) string {
	switch {
	case errors.Is(err, auth.ErrUnauthenticated):
		return "UNAUTHENTICATED"
	case errors.Is(err, identity.ErrNotFound):
		return "RESOURCE_NOT_FOUND"
	case errors.Is(err, identity.ErrBanned):
		return "USER_BANNED"
	case errors.Is(err, policy.ErrForbidden):
		return "FORBIDDEN"
	case errors.Is(err, policy.ErrInvalid):
		return "INVALID_REQUEST"
	case errors.Is(err, policy.ErrFeatureDisabled):
		return "FEATURE_DISABLED"
	case errors.Is(err, message.ErrIdempotencyConflict):
		return "IDEMPOTENCY_CONFLICT"
	case errors.Is(err, message.ErrResync):
		return "RESYNC_REQUIRED"
	default:
		return "DEPENDENCY_UNAVAILABLE"
	}
}
