package httpserver

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// receive: Ожидает событие с ограничением времени, чтобы ошибка синхронизации не подвесила весь suite.
func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for lifecycle event")
		var zero T
		return zero
	}
}

// listen: Выделяет свободный loopback port и регистрирует cleanup даже при досрочном завершении теста.
func listen(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

// TestShutdownDrainsInFlightRequestBeforeCancellingItsContext: Синхронизирует запрос каналами: сигнал приходит после входа handler, а ответ завершается во время drain.
func TestShutdownDrainsInFlightRequestBeforeCancellingItsContext(t *testing.T) {
	s := testServer(func(context.Context) error { return nil }, io.Discard)
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	s.Handle("/slow", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- r.Context()
		select {
		case <-release:
			_, _ = io.WriteString(w, "completed")
		case <-r.Context().Done():
		}
	}))
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	listener := listen(t)
	finished := make(chan error, 1)
	go func() { finished <- s.Run(ctx, listener) }()
	response := make(chan string, 1)
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		res, err := client.Get("http://" + listener.Addr().String() + "/slow")
		if err != nil {
			response <- "request failed"
			return
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		response <- string(body)
	}()
	requestContext := receive(t, entered)
	cancel()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for !s.draining.Load() {
		select {
		case <-deadline.C:
			t.Fatal("server did not start draining")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case <-requestContext.Done():
		t.Fatal("in-flight request was cancelled before drain timeout")
	default:
	}
	rr := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rr, httptest.NewRequest("GET", "/health/ready", nil))
	if rr.Code != 503 {
		t.Fatal("draining server must not remain ready")
	}
	unblock()
	if body := receive(t, response); body != "completed" {
		t.Fatalf("in-flight response lost: %q", body)
	}
	if err := receive(t, finished); err != nil {
		t.Fatal(err)
	}
	if requestContext.Err() == nil {
		t.Fatal("request context must be released after shutdown")
	}
}

// TestShutdownDeadlineCancelsOutstandingRequest: Удерживает handler до отмены context и проверяет принудительный shutdown после deadline.
func TestShutdownDeadlineCancelsOutstandingRequest(t *testing.T) {
	s := testServer(nil, io.Discard)
	s.shutdownTimeout = 50 * time.Millisecond
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	s.Handle("/blocked", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(cancelled)
	}))
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	listener := listen(t)
	finished := make(chan error, 1)
	go func() { finished <- s.Run(ctx, listener) }()
	clientDone := make(chan struct{})
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		res, err := client.Get("http://" + listener.Addr().String() + "/blocked")
		if err == nil {
			_ = res.Body.Close()
		}
		close(clientDone)
	}()
	receive(t, entered)
	cancel()
	if err := receive(t, finished); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("forced shutdown must report its deadline: %v", err)
	}
	receive(t, cancelled)
	receive(t, clientDone)
}

// TestServeFailureAndAlreadyCancelledStartup: Проверяет передачу ошибки Serve и освобождение listener при отменённом запуске.
func TestServeFailureAndAlreadyCancelledStartup(t *testing.T) {
	t.Run("listener failure is surfaced", func(t *testing.T) {
		s := testServer(nil, io.Discard)
		listener := listen(t)
		_ = listener.Close()
		if err := s.Run(t.Context(), listener); err == nil {
			t.Fatal("closed listener must fail")
		}
	})
	t.Run("cancelled start closes listener", func(t *testing.T) {
		s := testServer(nil, io.Discard)
		listener := listen(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := s.Run(ctx, listener); err != nil {
			t.Fatal(err)
		}
		if _, err := listener.Accept(); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("listener left open: %v", err)
		}
	})
}
