package realtime

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/internal/outbox"
	"github.com/redis/go-redis/v9"
)

// Hub contains routing/control handles only, never raw credentials or message content.
// Each Session owns its subscriptions, cursor and authenticated credential lifetime.
type Hub struct {
	mu          sync.Mutex
	connections map[*Connection]string
	draining    bool
	wg          sync.WaitGroup
}

func NewHub() *Hub { return &Hub{connections: map[*Connection]string{}} }
func (h *Hub) add(c *Connection) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.draining || len(h.connections) >= 10000 {
		return false
	}
	h.connections[c] = ""
	h.wg.Add(1)
	return true
}
func (h *Hub) identify(c *Connection, project string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.connections[c] = project
}
func (h *Hub) remove(c *Connection) {
	h.mu.Lock()
	delete(h.connections, c)
	h.mu.Unlock()
	h.wg.Done()
}

// Wake coalesces disposable hints. No event is dropped: the session re-reads PG.
func (h *Hub) Wake(project string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c, p := range h.connections {
		if p == project {
			select {
			case c.wake <- struct{}{}:
			default:
			}
		}
	}
}
func (h *Hub) Listen(ctx context.Context, r *redis.Client) {
	sub := r.Subscribe(ctx, outbox.HintChannel)
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case hint, ok := <-sub.Channel():
			if !ok {
				return
			}
			project, _, ok := strings.Cut(hint.Payload, ":")
			if ok {
				h.Wake(project)
			}
		}
	}
}

// Drain stops admission first, then asks each socket to send a control/close frame.
// Hijacked sockets are not drained by net/http.Shutdown, so app calls this explicitly.
func (h *Hub) Drain() {
	h.mu.Lock()
	h.draining = true
	connections := make([]*Connection, 0, len(h.connections))
	for c := range h.connections {
		connections = append(connections, c)
	}
	h.mu.Unlock()
	for _, c := range connections {
		c.stop(1001, "SERVER_DRAINING")
	}
	done := make(chan struct{})
	go func() { h.wg.Wait(); close(done) }()
	select {
	case <-done:
		return
	case <-time.After(5 * time.Second):
	}
	for _, c := range connections {
		c.socket.CloseNow()
		c.cancel()
	}
	<-done
}
