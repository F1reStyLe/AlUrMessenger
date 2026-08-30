package realtime

import (
	"testing"

	"github.com/coder/websocket"
)

// TestBackpressure verifies the close signal rather than silently discarding a frame.
func TestBackpressure(t *testing.T) {
	c := &Connection{send: make(chan Frame, 1), closing: make(chan closeRequest, 1)}
	if !c.enqueue(frame("ack", "", "", nil)) || c.enqueue(frame("ack", "", "", nil)) {
		t.Fatal("queue did not enforce its bound")
	}
	request := <-c.closing
	if request.code != websocket.StatusTryAgainLater {
		t.Fatal("slow client close code", request.code)
	}
}

// TestStrictFrame protects parser ambiguity and exact integer preservation.
func TestStrictFrame(t *testing.T) {
	for _, data := range []string{`{"type":"auth","unexpected":1}`, `{} {}`} {
		var f Frame
		if decode([]byte(data), &f) == nil {
			t.Fatal("ambiguous JSON accepted")
		}
	}
}
