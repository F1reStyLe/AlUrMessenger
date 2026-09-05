//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	neturl "net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/internal/auth"
	"github.com/F1reStyLe/AlUrMessenger/internal/conversation"
	conversationrepo "github.com/F1reStyLe/AlUrMessenger/internal/conversation/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/cryptography"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	identityrepo "github.com/F1reStyLe/AlUrMessenger/internal/identity/repository"
	identityhttp "github.com/F1reStyLe/AlUrMessenger/internal/identity/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/message"
	messagerepo "github.com/F1reStyLe/AlUrMessenger/internal/message/repository"
	messagehttp "github.com/F1reStyLe/AlUrMessenger/internal/message/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/admission"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/httpserver"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/infrastructure"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/F1reStyLe/AlUrMessenger/internal/provision"
	"github.com/F1reStyLe/AlUrMessenger/internal/realtime"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// leaseVerifier verifies signatures/expiry and models the external session revocation
// store. The live Auth HTTP compatibility is separately exercised in development.
type leaseVerifier struct {
	mu         sync.Mutex
	identities map[string]auth.Identity
	key        []byte
}

func (v *leaseVerifier) Verify(_ context.Context, token string) (auth.Identity, error) {
	parsed, err := jwt.Parse(token, func(*jwt.Token) (any, error) { return v.key, nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil || !parsed.Valid {
		return auth.Identity{}, auth.ErrUnauthenticated
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	id, ok := v.identities[token]
	if !ok {
		return auth.Identity{}, auth.ErrUnauthenticated
	}
	return id, nil
}

// testRealtime runs two independent HTTP servers/hubs on shared PG/Redis. There is
// deliberately no router loop: cross-instance delivery must recover without hints.
func testRealtime(t *testing.T, clients *infrastructure.Clients, op *pgx.Conn) {
	ctx := t.Context()
	project := uuid.NewString()
	if err := provision.Create(ctx, op, provision.Project{ID: project, Name: "Realtime", Issuer: "https://issuer.test", Audience: "chat"}); err != nil {
		t.Fatal(err)
	}
	verifier := &leaseVerifier{identities: map[string]auth.Identity{}, key: []byte("only-this-disposable-fixture-key-32")}
	identities := &identity.Service{Verifier: verifier, Store: &identityrepo.Store{DB: clients.Postgres}}
	tokenFor := func(external string, expires time.Time) string {
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: external, ID: uuid.NewString(), ExpiresAt: jwt.NewNumericDate(expires)}).SignedString(verifier.key)
		if err != nil {
			t.Fatal(err)
		}
		verifier.mu.Lock()
		verifier.identities[token] = auth.Identity{ProjectID: project, ExternalID: external}
		verifier.mu.Unlock()
		return token
	}
	aToken, bToken := tokenFor("alice", time.Now().Add(time.Hour)), tokenFor("bob", time.Now().Add(time.Hour))
	a, err := identities.Authenticate(ctx, aToken)
	if err != nil {
		t.Fatal(err)
	}
	b, err := identities.Authenticate(ctx, bToken)
	if err != nil {
		t.Fatal(err)
	}
	convStore := &conversationrepo.Store{DB: clients.Postgres}
	convs := &conversation.Service{Store: convStore}
	trace := policy.Trace{RequestID: uuid.NewString()}
	conv, _, err := convs.Create(ctx, a, conversation.Create{Type: "GROUP", MemberIDs: []string{b.User.ID}}, trace)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := cryptography.Generate()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := cryptography.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	store := &messagerepo.Store{DB: clients.Postgres, Crypto: keys}
	messages := &message.Service{Store: store}
	recovery := &message.RecoveryService{Store: store}
	presence := &realtime.Presence{DB: clients.Postgres, Redis: clients.Redis}
	start := func() (string, *realtime.Hub) {
		hub := realtime.NewHub()
		limiter := &admission.Limiter{Redis: clients.Redis, Config: admission.Config{Origins: map[string]bool{"https://demo.test": true}, IPPerMinute: 10000, UserPerMinute: 10000}}
		server := httpserver.New(config.HTTP{ReadHeaderTimeout: time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, MaxBodyBytes: 131072}, 2*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context) error { return nil })
		server.Handle("/ws", limiter.Before(&realtime.Gateway{Hub: hub, Identity: identities, Messages: messages, Recovery: recovery, Conversations: convs, Presence: presence, Limiter: limiter}))
		protect := func(next http.Handler) http.Handler {
			return limiter.Before(identityhttp.Authenticate(identities, limiter.After(next)))
		}
		messagehttp.Register(server, messages, protect)
		messagehttp.RegisterRecovery(server, recovery, protect)
		listener, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		run, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- server.Run(run, listener) }()
		t.Cleanup(func() {
			hub.Drain()
			cancel()
			if e := <-done; e != nil {
				t.Error(e)
			}
		})
		return "ws://" + listener.Addr().String() + "/ws", hub
	}
	first, hub1 := start()
	second, _ := start()
	dial := func(url, token string) *websocket.Conn {
		t.Helper()
		deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		socket, response, e := websocket.Dial(deadline, url, &websocket.DialOptions{Subprotocols: []string{"chat.v1"}, HTTPHeader: http.Header{"Origin": []string{"https://demo.test"}}})
		if e != nil {
			status := 0
			if response != nil {
				status = response.StatusCode
			}
			t.Fatal("WS dial", status, e)
		}
		t.Cleanup(func() { socket.CloseNow() })
		if e = wsjson.Write(deadline, socket, map[string]any{"type": "auth", "request_id": uuid.NewString(), "payload": map[string]any{"access_token": token, "device_id": uuid.NewString()}}); e != nil {
			t.Fatal(e)
		}
		var reply realtime.Frame
		if e = wsjson.Read(deadline, socket, &reply); e != nil || reply.Type != "auth.ok" {
			t.Fatal("auth reply", reply.Type, e)
		}
		return socket
	}
	receive := func(socket *websocket.Conn, kind string) realtime.Frame {
		t.Helper()
		deadline, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		for {
			var f realtime.Frame
			if e := wsjson.Read(deadline, socket, &f); e != nil {
				t.Fatal("waiting for", kind, e)
			}
			if f.Type == kind {
				return f
			}
			if f.Type == "error" {
				t.Fatal("unexpected error", string(f.Payload))
			}
		}
	}
	command := func(socket *websocket.Conn, kind string, p any) string {
		t.Helper()
		request := uuid.NewString()
		deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if e := wsjson.Write(deadline, socket, map[string]any{"type": kind, "request_id": request, "conversation_id": conv.ID, "payload": p}); e != nil {
			t.Fatal(e)
		}
		return request
	}
	subscribe := func(socket *websocket.Conn, after string) {
		command(socket, "conversation.subscribe", map[string]any{"after_event_sequence": after})
		receive(socket, "ack")
		receive(socket, "sync.complete")
	}
	alice := dial(first, aToken)
	bob := dial(second, bToken)
	subscribe(alice, "0")
	subscribe(bob, "0")
	send := message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: "cross instance"}}
	command(alice, "message.send", send)
	ack := receive(alice, "ack")
	var sent message.Sent
	if json.Unmarshal(ack.Payload, &sent) != nil || sent.Status != "SENT" || sent.Message.Sequence != 1 {
		t.Fatal("SENT receipt")
	}
	event := receive(bob, "message.created")
	if event.Sequence != "1" {
		t.Fatal("cross instance sequence", event.Sequence)
	}
	command(alice, "message.send", send)
	again := receive(alice, "ack")
	var duplicate message.Sent
	json.Unmarshal(again.Payload, &duplicate)
	if !duplicate.Deduplicated || duplicate.Message.ID != sent.Message.ID {
		t.Fatal("WS resend duplicated")
	}
	// REST and WebSocket share the same committed send receipt and projections.
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	request := func(method, path, body string, want int) []byte {
		t.Helper()
		url := "http" + strings.TrimSuffix(strings.TrimPrefix(first, "ws"), "/ws") + path
		req, e := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Authorization", "Bearer "+aToken)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		data, e := io.ReadAll(res.Body)
		if e != nil || res.StatusCode != want {
			t.Fatal("REST", method, path, res.StatusCode, want, string(data), e)
		}
		return data
	}
	collection := "/api/v1/conversations/" + conv.ID
	data, _ := json.Marshal(send)
	request("POST", collection+"/messages", string(data), 200)
	request("GET", "/api/v1/messages/"+sent.Message.ID, "", 200)
	request("GET", collection+"/messages?after_sequence=0&limit=1", "", 200)
	request("GET", collection+"/search?q=cross", "", 200)
	request("GET", collection+"/snapshot", "", 200)
	request("GET", collection+"/events?after_event_sequence=999", "", 409)
	request("POST", collection+"/read", "{}", 400)
	request("POST", collection+"/delivered", `{"sequence":"0"}`, 200)
	// Reconnect from last applied cursor does not replay the already applied message.
	bob.CloseNow()
	bob = dial(second, bToken)
	subscribe(bob, event.EventSequence)
	command(bob, "message.read", map[string]string{"sequence": "1"})
	readAck := receive(bob, "ack")
	var checkpoint message.Checkpoint
	json.Unmarshal(readAck.Payload, &checkpoint)
	if checkpoint.Read != 1 || checkpoint.Delivered != 1 {
		t.Fatal("READ does not imply delivered")
	}
	ownSecond := dial(first, bToken)
	subscribe(ownSecond, event.EventSequence)
	snap, err := recovery.Snapshot(ctx, b, conv.ID)
	if err != nil || snap.Checkpoint.Read != 1 || snap.MessageSequence != 1 || len(snap.Messages) != 1 {
		t.Fatal("snapshot inconsistent", err)
	}
	// Out-of-order receipts across devices cannot regress or claim future messages.
	if lower, e := recovery.Receipt(ctx, b, conv.ID, "read", 0); e != nil || lower.Read != 1 {
		t.Fatal("read regressed", e)
	}
	if _, e := recovery.Receipt(ctx, b, conv.ID, "delivered", 2); e != policy.ErrInvalid {
		t.Fatal("future receipt", e)
	}
	if _, e := recovery.Replay(ctx, b, conv.ID, 999, 50); e != message.ErrResync {
		t.Fatal("future replay cursor", e)
	}
	// Phase 5.1 shares optimistic edits, reply validation and soft delete between
	// REST and both WS instances. Events hydrate current state rather than old text.
	path := "/api/v1/messages/" + sent.Message.ID
	request("PATCH", path, `{"content":{"text":"restedit"},"expected_version":"1"}`, 200)
	changed := receive(bob, "message.updated")
	var hydrated struct {
		Message message.Message `json:"message"`
	}
	if json.Unmarshal(changed.Payload, &hydrated) != nil || hydrated.Message.Version != 2 || hydrated.Message.Content.Text != "restedit" {
		t.Fatal("REST edit not hydrated over WS")
	}
	request("PATCH", path, `{"content":{"text":"stale"},"expected_version":"1"}`, 409)
	request("PATCH", path, `{"content":{"text":"missing version"}}`, 400)
	command(alice, "message.edit", map[string]any{"message_id": sent.Message.ID, "content": message.Content{Text: "wsedit"}, "expected_version": "2"})
	changed = receive(alice, "ack")
	var current message.Message
	if json.Unmarshal(changed.Payload, &current) != nil || current.Version != 3 || current.Content.Text != "wsedit" {
		t.Fatal("WS edit ack")
	}
	receive(bob, "message.updated")
	command(alice, "message.edit", map[string]any{"message_id": sent.Message.ID, "content": message.Content{Text: "stale"}, "expected_version": "2"})
	conflict := receive(alice, "error")
	var commandError map[string]any
	json.Unmarshal(conflict.Payload, &commandError)
	if commandError["code"] != "VERSION_CONFLICT" {
		t.Fatal("WS stale version", commandError)
	}
	// Envelope/resource mismatch is rejected before deletion, even for the author.
	wrongScope := uuid.NewString()
	if e := wsjson.Write(ctx, alice, map[string]any{"type": "message.delete", "request_id": uuid.NewString(), "conversation_id": wrongScope, "payload": map[string]string{"message_id": sent.Message.ID}}); e != nil {
		t.Fatal(e)
	}
	wrongScopeReply := receive(alice, "error")
	json.Unmarshal(wrongScopeReply.Payload, &commandError)
	if commandError["code"] != "RESOURCE_NOT_FOUND" {
		t.Fatal("WS mutation ignored envelope scope")
	}
	command(bob, "message.delete", map[string]string{"message_id": sent.Message.ID})
	deniedAuthor := receive(bob, "error")
	json.Unmarshal(deniedAuthor.Payload, &commandError)
	if commandError["code"] != "FORBIDDEN" {
		t.Fatal("WS deleted another author's message")
	}
	replyCommand := message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: "replybody"}, ReplyTo: &sent.Message.ID}
	command(alice, "message.send", replyCommand)
	replyAck := receive(alice, "ack")
	var replied message.Sent
	if json.Unmarshal(replyAck.Payload, &replied) != nil || replied.Message.Reply == nil || replied.Message.Reply.MessageID != sent.Message.ID {
		t.Fatal("WS reply reference")
	}
	receive(bob, "message.created")
	command(alice, "message.delete", map[string]string{"message_id": sent.Message.ID})
	deleteAck := receive(alice, "ack")
	current = message.Message{}
	if e := json.Unmarshal(deleteAck.Payload, &current); e != nil {
		t.Fatal(e)
	}
	assertTombstone(t, current, nil, "deleted")
	removed := receive(bob, "message.deleted")
	hydrated.Message = message.Message{}
	if e := json.Unmarshal(removed.Payload, &hydrated); e != nil {
		t.Fatal(e)
	}
	assertTombstone(t, hydrated.Message, nil, "deleted")
	if e := json.Unmarshal(request("GET", path, "", 200), &current); e != nil {
		t.Fatal(e)
	}
	assertTombstone(t, current, nil, "deleted")
	data, _ = json.Marshal(send)
	duplicate = message.Sent{}
	if e := json.Unmarshal(request("POST", collection+"/messages", string(data), 200), &duplicate); e != nil {
		t.Fatal(e)
	}
	assertTombstone(t, duplicate.Message, nil, "deleted")
	// A live reply still resolves the parent's current tombstone, never a snapshot
	// of the parent's former text. The reply's independent body remains available.
	var liveReply message.Message
	if e := json.Unmarshal(request("GET", "/api/v1/messages/"+replied.Message.ID, "", 200), &liveReply); e != nil || liveReply.Reply == nil || liveReply.Content.Text != "replybody" {
		t.Fatal("reply after parent delete", e)
	}
	request("DELETE", "/api/v1/messages/"+replied.Message.ID, "", 200)
	receive(bob, "message.deleted")
	// Reconnecting from the beginning must hydrate even message.created as deleted.
	fresh := dial(second, aToken)
	command(fresh, "conversation.subscribe", map[string]string{"after_event_sequence": "0"})
	receive(fresh, "ack")
	oldCreated := receive(fresh, "message.created")
	var redacted struct {
		Message message.Message `json:"message"`
	}
	if e := json.Unmarshal(oldCreated.Payload, &redacted); e != nil {
		t.Fatal(e)
	}
	assertTombstone(t, redacted.Message, nil, "deleted")
	fresh.CloseNow()
	// CORS permits the implemented item mutations, without enabling future subroutes.
	url := "http" + strings.TrimSuffix(strings.TrimPrefix(first, "ws"), "/ws") + path
	for _, method := range []string{"PATCH", "DELETE"} {
		req, e := http.NewRequestWithContext(ctx, "OPTIONS", url, nil)
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Origin", "https://demo.test")
		req.Header.Set("Access-Control-Request-Method", method)
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		res.Body.Close()
		if res.StatusCode != 204 {
			t.Fatal("message CORS", res.StatusCode)
		}
	}
	// Phase 5.2: REST associations are visible on the other API; WS reaction
	// retries share the same uniqueness and current-state recovery contract.
	relationCommand := message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: "relationwire"}}
	data, _ = json.Marshal(relationCommand)
	var relationSent message.Sent
	if e := json.Unmarshal(request("POST", collection+"/messages", string(data), 201), &relationSent); e != nil {
		t.Fatal(e)
	}
	receive(bob, "message.created")
	relationPath := "/api/v1/messages/" + relationSent.Message.ID
	reactionPath := relationPath + "/reactions/" + neturl.PathEscape("❤")
	request("PUT", reactionPath, "", 200)
	added := receive(bob, "reaction.created")
	var relationState struct {
		Message message.Message `json:"message"`
	}
	if e := json.Unmarshal(added.Payload, &relationState); e != nil || len(relationState.Message.Reactions) != 1 || relationState.Message.Reactions[0].Count != 1 {
		t.Fatal("reaction WS hydration", e)
	}
	command(bob, "reaction.add", map[string]string{"message_id": relationSent.Message.ID, "reaction": "❤️"})
	reactionAck := receive(bob, "ack")
	var reacted message.Message
	if e := json.Unmarshal(reactionAck.Payload, &reacted); e != nil || reacted.Version != 3 || reacted.Reactions[0].Count != 2 {
		t.Fatal("reaction ack", e)
	}
	command(bob, "reaction.add", map[string]string{"message_id": relationSent.Message.ID, "reaction": "❤"})
	reactionAck = receive(bob, "ack")
	if e := json.Unmarshal(reactionAck.Payload, &reacted); e != nil || reacted.Version != 3 {
		t.Fatal("reaction retry", e)
	}
	command(bob, "reaction.remove", map[string]string{"message_id": relationSent.Message.ID, "reaction": "❤️"})
	reactionAck = receive(bob, "ack")
	if e := json.Unmarshal(reactionAck.Payload, &reacted); e != nil || reacted.Reactions[0].Count != 1 {
		t.Fatal("reaction remove", e)
	}
	request("PUT", relationPath+"/reactions/not-emoji", "", 400)
	if _, e := op.Exec(ctx, "UPDATE chat.project_settings SET flags=jsonb_set(flags,'{allow_reactions}','false') WHERE project_id=$1", project); e != nil {
		t.Fatal(e)
	}
	command(bob, "reaction.add", map[string]string{"message_id": relationSent.Message.ID, "reaction": "👍"})
	flagError := receive(bob, "error")
	json.Unmarshal(flagError.Payload, &commandError)
	if commandError["code"] != "FEATURE_DISABLED" {
		t.Fatal("WS reaction flag")
	}
	request("DELETE", reactionPath, "", 403)
	if _, e := op.Exec(ctx, "UPDATE chat.project_settings SET flags=jsonb_set(flags,'{allow_reactions}','true') WHERE project_id=$1", project); e != nil {
		t.Fatal(e)
	}
	pinPath := collection + "/pins/" + relationSent.Message.ID
	var pin message.Pin
	if e := json.Unmarshal(request("PUT", pinPath, "", 200), &pin); e != nil || pin.MessageID != relationSent.Message.ID {
		t.Fatal("pin REST", e)
	}
	receive(bob, "message.pinned")
	var pinList message.PinList
	if e := json.Unmarshal(request("GET", collection+"/pins", "", 200), &pinList); e != nil || len(pinList.Items) != 1 {
		t.Fatal("pins collection", e)
	}
	request("DELETE", pinPath, "", 204)
	receive(bob, "message.unpinned")
	request("DELETE", pinPath, "", 204)
	request("PUT", pinPath, "", 200)
	receive(bob, "message.pinned")
	request("DELETE", relationPath, "", 200)
	receive(bob, "message.deleted")
	// Old relation events must resolve to the deleted state on reconnect.
	reconnected := dial(second, bToken)
	command(reconnected, "conversation.subscribe", map[string]string{"after_event_sequence": "0"})
	receive(reconnected, "ack")
	replayedRelation := receive(reconnected, "reaction.created")
	relationState.Message = message.Message{}
	if e := json.Unmarshal(replayedRelation.Payload, &relationState); e != nil {
		t.Fatal(e)
	}
	assertTombstone(t, relationState.Message, nil, "deleted")
	if len(relationState.Message.Reactions) != 0 || relationState.Message.Pin != nil {
		t.Fatal("reconnected relations resurrected")
	}
	reconnected.CloseNow()
	for _, item := range []string{reactionPath, pinPath} {
		for _, method := range []string{"PUT", "DELETE"} {
			req, e := http.NewRequestWithContext(ctx, "OPTIONS", "http"+strings.TrimSuffix(strings.TrimPrefix(first, "ws"), "/ws")+item, nil)
			if e != nil {
				t.Fatal(e)
			}
			req.Header.Set("Origin", "https://demo.test")
			req.Header.Set("Access-Control-Request-Method", method)
			res, e := client.Do(req)
			if e != nil {
				t.Fatal(e)
			}
			res.Body.Close()
			if res.StatusCode != 204 {
				t.Fatal("relations CORS", res.StatusCode)
			}
		}
	}
	// A disabled flag hides peer receipts without creating a gap in the event sequence.
	if _, err = op.Exec(ctx, "UPDATE chat.project_settings SET flags=jsonb_set(flags,'{read_receipts}','false') WHERE project_id=$1", project); err != nil {
		t.Fatal(err)
	}
	replay, err := recovery.Replay(ctx, a, conv.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	hidden := false
	for _, e := range replay.Events {
		if e.Type == "sync.advance" {
			hidden = true
		}
	}
	if !hidden {
		t.Fatal("disabled receipt exposed")
	}
	// Lease deletion must leave another device online; TTL expires crashed connections.
	fake1, fake2 := uuid.NewString(), uuid.NewString()
	if err = presence.Touch(ctx, a, fake1, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if err = presence.Touch(ctx, a, fake2, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	presence.Drop(ctx, a, fake1)
	statuses, err := presence.Statuses(ctx, b, conv.ID, []string{a.User.ID})
	if err != nil || len(statuses) != 1 || !statuses[0].Online {
		t.Fatal("multi-device presence", err)
	}
	if err = presence.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	var seen *time.Time
	if err = op.QueryRow(ctx, "SELECT last_seen_at FROM chat.users WHERE id=$1", a.User.ID).Scan(&seen); err != nil || seen == nil {
		t.Fatal("last seen flush", err)
	}
	if err = presence.Typing(ctx, a, conv.ID, fake2, true); err != nil {
		t.Fatal(err)
	}
	typers, err := presence.Typers(ctx, b, conv.ID)
	if err != nil || len(typers) != 1 || typers[0] != a.User.ID {
		t.Fatal("typing lease", err)
	}
	if err = presence.Typing(ctx, a, conv.ID, fake2, false); err != nil {
		t.Fatal(err)
	}
	// Stop only this script-owned Redis fixture, then restore it for later checks.
	fixture(t, "stop", "redis")
	bounded, stopRedisCheck := context.WithTimeout(ctx, 2*time.Second)
	if _, e := presence.Statuses(bounded, b, conv.ID, []string{a.User.ID}); e == nil {
		t.Fatal("Redis outage fabricated presence")
	}
	stopRedisCheck()
	if _, e := recovery.Snapshot(ctx, b, conv.ID); e != nil {
		t.Fatal("Redis outage broke durable recovery", e)
	}
	fixture(t, "start", "redis")
	eventually(t, func(ctx context.Context) bool { return clients.Redis.Ping(ctx).Err() == nil })
	// Leave revokes all devices; no subsequent reference/content may be delivered.
	memberships := &conversation.MembershipService{Store: convStore}
	if err = memberships.Remove(ctx, b, conv.ID, b.User.ID, trace); err != nil {
		t.Fatal(err)
	}
	receive(bob, "subscription.revoked")
	receive(ownSecond, "subscription.revoked")
	command(bob, "message.send", message.Send{ClientID: uuid.NewString(), Type: "TEXT", Content: message.Content{Text: "forbidden"}})
	denied := receive(bob, "error")
	var code map[string]any
	json.Unmarshal(denied.Payload, &code)
	if code["code"] != "RESOURCE_NOT_FOUND" {
		t.Fatal("leave allowed send")
	}
	// Logout invalidates an already-open socket, even when it has no new commands.
	verifier.mu.Lock()
	delete(verifier.identities, bToken)
	verifier.mu.Unlock()
	deadline, cancel := context.WithTimeout(ctx, 6*time.Second)
	for {
		_, _, e := bob.Read(deadline)
		if e != nil {
			if websocket.CloseStatus(e) != 4401 {
				t.Fatal("revoked close", e)
			}
			break
		}
	}
	cancel()
	// Expiry timer closes another valid session independently of traffic.
	short := tokenFor("alice", time.Now().Add(3*time.Second))
	expiring := dial(second, short)
	deadline, cancel = context.WithTimeout(ctx, 7*time.Second)
	for {
		_, _, e := expiring.Read(deadline)
		if e != nil {
			if websocket.CloseStatus(e) != 4401 {
				t.Fatal("expiry close", e)
			}
			break
		}
	}
	cancel()
	// Origin/query-token boundaries fail before authentication/upgrading.
	for _, item := range []struct{ url, origin string }{{first, "https://evil.test"}, {first, ""}, {first + "?access_token=forbidden", "https://demo.test"}} {
		deadline, cancel = context.WithTimeout(ctx, 3*time.Second)
		socket, response, e := websocket.Dial(deadline, item.url, &websocket.DialOptions{Subprotocols: []string{"chat.v1"}, HTTPHeader: http.Header{"Origin": []string{item.origin}}})
		cancel()
		if e == nil {
			socket.CloseNow()
			t.Fatal("unsafe handshake accepted")
		}
		if response == nil || (response.StatusCode != 403 && response.StatusCode != 400) {
			t.Fatal("unsafe handshake status")
		}
	}
	// Explicit Hub drain closes hijacked sockets and joins their goroutines.
	drained := make(chan struct{})
	go func() { hub1.Drain(); close(drained) }()
	receive(alice, "server.draining")
	select {
	case <-drained:
	case <-time.After(8 * time.Second):
		t.Fatal("WS drain hung")
	}
}
