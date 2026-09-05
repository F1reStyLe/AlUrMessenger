package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/internal/auth"
	"github.com/F1reStyLe/AlUrMessenger/internal/conversation"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/message"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/coder/websocket"
)

// Session owns one credential and its state until disconnect. Hub/Redis/Kafka never
// receive the token. Maps belong exclusively to run; writer reads immutable identity.
type Session struct {
	gateway                    *Gateway
	connection                 *Connection
	actor                      identity.Actor
	token, device, id          string
	expires                    time.Time
	subscriptions              map[string]int64
	presenceWatch              map[string][]string
	presenceState, typingState map[string]string
}

// verify asks the external authority again; no positive cache survives logout.
// Outbound frames and commands fail closed during Auth outages, closing with 1013.
func (s *Session) verify(ctx context.Context) error {
	if !time.Now().Before(s.expires) {
		return auth.ErrUnauthenticated
	}
	id, err := s.gateway.Identity.Verifier.Verify(ctx, s.token)
	if err != nil {
		return err
	}
	if id.ProjectID != s.actor.ProjectID || id.ExternalID != s.actor.ExternalID {
		return auth.ErrUnauthenticated
	}
	return nil
}
func (s *Session) authenticationFailure(err error) {
	if errors.Is(err, auth.ErrUnauthenticated) {
		s.connection.stop(4401, "UNAUTHENTICATED")
	} else {
		s.connection.stop(1013, "AUTH_UNAVAILABLE")
	}
}
func (s *Session) replyError(f Frame, err error) {
	s.connection.enqueue(frame("error", f.RequestID, f.ConversationID, map[string]any{"code": errorCode(err), "message": errorCode(err)}))
}

// run joins every goroutine before returning; only the connection owner closes it.
func (s *Session) run(ctx context.Context, requestID string) {
	c := s.connection
	var wg sync.WaitGroup
	wg.Go(func() { s.read(ctx) })
	wg.Go(func() { s.write(ctx) })
	wg.Go(func() { s.heartbeat(ctx) })
	defer func() { c.cancel(); c.socket.CloseNow(); wg.Wait() }()
	c.enqueue(frame("auth.ok", requestID, "", map[string]any{"connection_id": s.id, "device_id": s.device, "project_id": s.actor.ProjectID, "user_id": s.actor.User.ID, "protocol_version": 1, "heartbeat_interval_ms": 25000, "token_expires_at": s.expires}))
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	expiry := time.NewTimer(time.Until(s.expires))
	defer expiry.Stop()
	warned := false
	ticks := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-expiry.C:
			c.stop(4401, "TOKEN_EXPIRED")
			returnAfterClose(ctx, c)
			return
		case f := <-c.incoming:
			bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := s.verify(bounded)
			if err == nil {
				allowed, _, rateErr := s.gateway.Limiter.Take(bounded, "user:"+s.actor.ProjectID+":"+s.actor.User.ID, s.gateway.Limiter.Config.UserPerMinute)
				if rateErr != nil {
					err = rateErr
				} else if !allowed {
					c.enqueue(frame("error", f.RequestID, f.ConversationID, map[string]any{"code": "RATE_LIMITED", "message": "Command quota exceeded"}))
					cancel()
					continue
				}
			}
			if err != nil {
				cancel()
				s.authenticationFailure(err)
				returnAfterClose(ctx, c)
				return
			}
			s.command(bounded, f)
			cancel()
		case <-c.wake:
			s.catchup(ctx)
		case <-poll.C:
			bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := s.verify(bounded)
			cancel()
			if err != nil {
				s.authenticationFailure(err)
				returnAfterClose(ctx, c)
				return
			}
			if !warned && time.Until(s.expires) <= time.Minute {
				c.enqueue(frame("auth.expiring", "", "", map[string]any{"expires_at": s.expires}))
				warned = true
			}
			s.catchup(ctx)
			ticks++
			if ticks%2 == 0 {
				s.ephemeral(ctx)
			}
		}
	}
}

// returnAfterClose gives the writer ownership of the close frame before run cleanup.
func returnAfterClose(ctx context.Context, c *Connection) {
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		c.socket.CloseNow()
		c.cancel()
	}
}

// read keeps control frames/pongs moving even while a command executes SQL.
func (s *Session) read(ctx context.Context) {
	c := s.connection
	for {
		typ, data, err := c.socket.Read(ctx)
		if err != nil {
			c.cancel()
			return
		}
		var f Frame
		if typ != websocket.MessageText || decode(data, &f) != nil || !conversation.ValidID(f.RequestID) || f.EventID != "" || f.EventSequence != "" || f.Sequence != "" || f.Type == "auth" {
			c.stop(1008, "INVALID_FRAME")
			return
		}
		select {
		case c.incoming <- f:
		case <-ctx.Done():
			return
		default:
			c.stop(1013, "COMMAND_BACKPRESSURE")
			return
		}
	}
}

// write verifies the credential and current membership immediately before delivery.
// Durable references are re-read to apply current privacy flags to already queued data.
func (s *Session) write(ctx context.Context) {
	c := s.connection
	defer c.cancel()
	closeSocket := func(request closeRequest) {
		if request.code == 1001 {
			bounded, cancel := context.WithTimeout(context.Background(), time.Second)
			data, _ := json.Marshal(frame("server.draining", "", "", map[string]any{"retry_after_ms": 1000}))
			c.socket.Write(bounded, websocket.MessageText, data)
			cancel()
		}
		c.socket.Close(request.code, request.reason)
	}
	for {
		select {
		case request := <-c.closing:
			closeSocket(request)
			return
		default:
		}
		select {
		case <-ctx.Done():
			return
		case request := <-c.closing:
			closeSocket(request)
			return
		case f := <-c.send:
			bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := s.verify(bounded)
			if err != nil {
				cancel()
				code := websocket.StatusCode(1013)
				if errors.Is(err, auth.ErrUnauthenticated) {
					code = 4401
				}
				closeSocket(closeRequest{code, errorCode(err)})
				return
			}
			if f.ConversationID != "" && f.Type != "error" && f.Type != "subscription.revoked" && f.Type != "resync.required" {
				if _, err = s.gateway.Conversations.Get(bounded, s.actor, f.ConversationID); err != nil {
					f = frame("subscription.revoked", "", f.ConversationID, map[string]any{"reason_code": errorCode(err)})
				} else if f.EventSequence != "" {
					seq, _ := strconv.ParseInt(f.EventSequence, 10, 64)
					replay, e := s.gateway.Recovery.Replay(bounded, s.actor, f.ConversationID, seq-1, 1)
					// A leave can commit between the metadata check above and the
					// authoritative replay read. Report revocation, not an outage,
					// and discard all previously queued message content in this frame.
					if errors.Is(e, identity.ErrNotFound) {
						f = frame("subscription.revoked", "", f.ConversationID, map[string]any{"reason_code": errorCode(e)})
					} else if errors.Is(e, message.ErrResync) {
						f = frame("resync.required", "", f.ConversationID, map[string]any{"reason_code": errorCode(e)})
					} else if e != nil || len(replay.Events) != 1 {
						cancel()
						c.stop(1013, "REPLAY_UNAVAILABLE")
						continue
					} else {
						event := replay.Events[0]
						f.Type = event.Type
						f.Payload, _ = json.Marshal(event.Payload)
					}
				}
			}
			// Recompute ephemeral state at delivery as well: a queued presence
			// frame must not reveal stale data after a privacy flag changed.
			// A committed send/edit ack may wait behind other frames while another
			// device deletes the message. Refresh its DTO before writing the socket.
			if f.Type == "ack" && f.MessageReply != "" {
				f, err = s.refreshMessageReply(bounded, f)
				if err != nil {
					cancel()
					c.stop(1013, "MESSAGE_UNAVAILABLE")
					continue
				}
			}
			if f.Type == "presence.state" || f.PresenceReply {
				var statuses []Status
				if f.PresenceReply {
					json.Unmarshal(f.Payload, &statuses)
				} else {
					var payload struct {
						Users []Status `json:"users"`
					}
					json.Unmarshal(f.Payload, &payload)
					statuses = payload.Users
				}
				ids := []string{}
				for _, status := range statuses {
					ids = append(ids, status.UserID)
				}
				current, e := s.gateway.Presence.Statuses(bounded, s.actor, f.ConversationID, ids)
				if f.PresenceReply && e == nil {
					f.Payload, _ = json.Marshal(current)
				} else {
					payload := map[string]any{"available": e == nil, "users": current}
					if e != nil {
						payload["code"] = errorCode(e)
					}
					f.Payload, _ = json.Marshal(payload)
				}
			}
			if f.Type == "typing.state" {
				current, e := s.gateway.Presence.Typers(bounded, s.actor, f.ConversationID)
				payload := map[string]any{"available": e == nil, "user_ids": current, "expires_in_ms": 5000}
				if e != nil {
					payload["code"] = errorCode(e)
				}
				f.Payload, _ = json.Marshal(payload)
			}
			data, e := json.Marshal(f)
			if e == nil {
				e = c.socket.Write(bounded, websocket.MessageText, data)
			}
			cancel()
			if e != nil {
				c.socket.Close(1013, "SLOW_CONSUMER")
				return
			}
		}
	}
}

// heartbeat requires a protocol pong before lease renewal; a dead peer cannot remain
// online because its server goroutine merely continues running.
func (s *Session) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := s.connection.socket.Ping(bounded)
			if err == nil {
				err = s.gateway.Presence.Touch(bounded, s.actor, s.id, s.device)
			}
			cancel()
			if err != nil {
				s.connection.stop(1013, "HEARTBEAT_UNAVAILABLE")
				return
			}
		}
	}
}

// command delegates durable writes to the same application services as REST.
func (s *Session) command(ctx context.Context, f Frame) {
	c := s.connection
	if !conversation.ValidID(f.ConversationID) {
		s.replyError(f, policy.ErrInvalid)
		return
	}
	var result any
	var err error
	switch f.Type {
	case "conversation.subscribe":
		var p struct {
			After int64 `json:"after_event_sequence,string"`
		}
		if decode(f.Payload, &p) != nil || p.After < 0 {
			err = policy.ErrInvalid
			break
		}
		if _, exists := s.subscriptions[f.ConversationID]; !exists && len(s.subscriptions) >= 16 {
			err = policy.ErrInvalid
			break
		}
		if _, err = s.gateway.Recovery.Replay(ctx, s.actor, f.ConversationID, p.After, 1); err != nil {
			break
		}
		s.subscriptions[f.ConversationID] = p.After
		result = map[string]any{"subscribed": true}
	case "conversation.unsubscribe":
		var p struct{}
		if decode(f.Payload, &p) != nil {
			err = policy.ErrInvalid
			break
		}
		delete(s.subscriptions, f.ConversationID)
		delete(s.presenceWatch, f.ConversationID)
		delete(s.presenceState, f.ConversationID)
		delete(s.typingState, f.ConversationID)
		result = map[string]any{"subscribed": false}
	case "message.send":
		var p message.Send
		if decode(f.Payload, &p) != nil {
			err = policy.ErrInvalid
			break
		}
		result, err = s.gateway.Messages.Send(ctx, s.actor, f.ConversationID, p)
	case "message.edit":
		var p struct {
			MessageID string `json:"message_id"`
			message.Edit
		}
		if decode(f.Payload, &p) != nil {
			err = policy.ErrInvalid
			break
		}
		result, err = s.gateway.Messages.Edit(ctx, s.actor, p.MessageID, f.ConversationID, p.Edit)
	case "message.delete":
		var p struct {
			MessageID string `json:"message_id"`
		}
		if decode(f.Payload, &p) != nil {
			err = policy.ErrInvalid
			break
		}
		result, err = s.gateway.Messages.Delete(ctx, s.actor, p.MessageID, f.ConversationID)
	case "message.read", "message.delivered":
		var p struct {
			Sequence *int64 `json:"sequence,string"`
		}
		if decode(f.Payload, &p) != nil || p.Sequence == nil {
			err = policy.ErrInvalid
			break
		}
		kind := "read"
		if f.Type == "message.delivered" {
			kind = "delivered"
		}
		result, err = s.gateway.Recovery.Receipt(ctx, s.actor, f.ConversationID, kind, *p.Sequence)
	case "typing.start", "typing.stop":
		var p struct{}
		if decode(f.Payload, &p) != nil {
			err = policy.ErrInvalid
			break
		}
		err = s.gateway.Presence.Typing(ctx, s.actor, f.ConversationID, s.id, f.Type == "typing.start")
		result = map[string]any{"expires_in_ms": 5000}
	case "presence.watch":
		var p struct {
			Users []string `json:"user_ids"`
		}
		if decode(f.Payload, &p) != nil {
			err = policy.ErrInvalid
			break
		}
		if _, exists := s.subscriptions[f.ConversationID]; !exists {
			err = policy.ErrInvalid
			break
		}
		result, err = s.gateway.Presence.Statuses(ctx, s.actor, f.ConversationID, p.Users)
		if err == nil {
			s.presenceWatch[f.ConversationID] = p.Users
		}
	default:
		err = policy.ErrInvalid
	}
	if err != nil {
		s.replyError(f, err)
		return
	}
	reply := frame("ack", f.RequestID, f.ConversationID, result)
	reply.PresenceReply = f.Type == "presence.watch"
	if f.Type == "message.send" || f.Type == "message.edit" || f.Type == "message.delete" {
		reply.MessageReply = f.Type
	}
	c.enqueue(reply)
	if f.Type == "conversation.subscribe" {
		s.replay(ctx, f.ConversationID, true)
	}
}

// refreshMessageReply preserves receipt identity while replacing only its mutable
// message state. A membership revocation must not leave the old ack body in flight.
func (s *Session) refreshMessageReply(ctx context.Context, f Frame) (Frame, error) {
	var sent message.Sent
	var m message.Message
	var err error
	if f.MessageReply == "message.send" {
		err = json.Unmarshal(f.Payload, &sent)
		m = sent.Message
	} else {
		err = json.Unmarshal(f.Payload, &m)
	}
	if err != nil {
		return Frame{}, err
	}
	m, err = s.gateway.Messages.Get(ctx, s.actor, m.ID)
	if errors.Is(err, identity.ErrNotFound) {
		return frame("subscription.revoked", "", f.ConversationID, map[string]any{"reason_code": errorCode(err)}), nil
	}
	if err != nil {
		return Frame{}, err
	}
	if f.MessageReply == "message.send" {
		sent.Message = m
		f.Payload, err = json.Marshal(sent)
	} else {
		f.Payload, err = json.Marshal(m)
	}
	return f, err
}

// replay advances only contiguous committed events. Enqueue is not delivery: clients
// persist only applied cursors and recover again after any socket/write failure.
func (s *Session) replay(ctx context.Context, id string, initial bool) {
	// Large catch-ups wait for the bounded writer queue to drain rather than
	// overflowing solely because replay can read PostgreSQL faster than the network.
	if len(s.connection.send) > 16 && !initial {
		return
	}
	after := s.subscriptions[id]
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := s.gateway.Recovery.Replay(bounded, s.actor, id, after, 32)
	if err != nil {
		kind := "subscription.revoked"
		payload := map[string]any{"reason_code": errorCode(err)}
		if errors.Is(err, message.ErrResync) {
			kind = "resync.required"
			payload["snapshot_url"] = "/api/v1/conversations/" + id + "/snapshot"
		}
		if !errors.Is(err, message.ErrResync) && !errors.Is(err, identity.ErrNotFound) {
			s.connection.stop(1013, "REPLAY_UNAVAILABLE")
			return
		}
		delete(s.subscriptions, id)
		delete(s.presenceWatch, id)
		s.connection.enqueue(frame(kind, "", id, payload))
		return
	}
	for _, e := range result.Events {
		f := frame(e.Type, "", id, e.Payload)
		f.EventID = e.ID
		f.EventSequence = strconv.FormatInt(e.Sequence, 10)
		if seq, ok := e.Payload["message_sequence"].(string); ok {
			f.Sequence = seq
		}
		if !s.connection.enqueue(f) {
			return
		}
		s.subscriptions[id] = e.Sequence
	}
	if !result.More && (initial || result.Through > after) {
		s.connection.enqueue(frame("sync.complete", "", id, map[string]any{"through_event_sequence": strconv.FormatInt(result.Through, 10), "has_more": false}))
	}
	if result.More {
		select {
		case s.connection.wake <- struct{}{}:
		default:
		}
	}
}
func (s *Session) catchup(ctx context.Context) {
	for id := range s.subscriptions {
		if ctx.Err() != nil {
			return
		}
		s.replay(ctx, id, false)
	}
}

// ephemeral sends bounded snapshots on change. Disabled/unavailable state clears
// cached UI signals explicitly; it must not masquerade as all users going offline.
func (s *Session) ephemeral(ctx context.Context) {
	for id := range s.subscriptions {
		bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
		typers, err := s.gateway.Presence.Typers(bounded, s.actor, id)
		payload := map[string]any{"available": err == nil, "user_ids": typers, "expires_in_ms": 5000}
		if err != nil {
			payload["code"] = errorCode(err)
		}
		raw, _ := json.Marshal(payload)
		if string(raw) != s.typingState[id] {
			s.connection.enqueue(frame("typing.state", "", id, payload))
			s.typingState[id] = string(raw)
		}
		if users, ok := s.presenceWatch[id]; ok {
			statuses, e := s.gateway.Presence.Statuses(bounded, s.actor, id, users)
			p := map[string]any{"available": e == nil, "users": statuses}
			if e != nil {
				p["code"] = errorCode(e)
			}
			data, _ := json.Marshal(p)
			if string(data) != s.presenceState[id] {
				s.connection.enqueue(frame("presence.state", "", id, p))
				s.presenceState[id] = string(data)
			}
		}
		cancel()
	}
}
