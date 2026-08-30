// Package httpserver owns the HTTP transport and its bounded lifecycle.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
)

// Readiness checks the dependencies needed by this service, honoring ctx.
// A missing check means unconfigured, never implicitly ready.
// Callback должен сам соблюдать deadline: вызов синхронный, принудительного
// прерывания произвольной функции по context в Go нет.
type Readiness func(ctx context.Context) error

// Server владеет router и HTTP lifecycle одного запуска. Регистрировать routes нужно
// до Run. Состояние drain читается параллельно handler-ами, поэтому используется atomic.
type Server struct {
	http            *http.Server
	mux             *http.ServeMux
	logger          *slog.Logger
	shutdownTimeout time.Duration
	draining        atomic.Bool
}

// New собирает transport без открытия порта; cfg уже должен пройти startup validation.
// Оба probe публичны и не зависят от будущего JWT middleware бизнес-маршрутов.
func New(cfg config.HTTP, shutdownTimeout time.Duration, logger *slog.Logger, ready Readiness) *Server {
	s := &Server{mux: http.NewServeMux(), logger: logger, shutdownTimeout: shutdownTimeout}
	s.mux.HandleFunc("/health/live", readOnly(func(w http.ResponseWriter, r *http.Request) {
		// Liveness показывает работоспособность процесса, а не внешних зависимостей.
		WriteJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
	}))
	s.mux.HandleFunc("/health/ready", readOnly(func(w http.ResponseWriter, r *http.Request) {
		if s.draining.Load() {
			WriteError(w, r, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Service is draining")
			return
		}
		if ready == nil {
			WriteError(w, r, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Service dependencies are not configured")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		// Пока callback выполнялся, мог начаться drain или истечь deadline.
		// Повторная проверка не позволяет опираться только на устаревший результат callback.
		if err := ready(ctx); err != nil || ctx.Err() != nil || s.draining.Load() {
			WriteError(w, r, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Service dependencies are unavailable")
			return
		}
		WriteJSON(w, r, http.StatusOK, map[string]string{"status": "ready"})
	}))
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, http.StatusNotFound, "RESOURCE_NOT_FOUND", "Resource not found")
	})
	admission := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Проверка нужна и для уже открытых соединений; закрытие listener само по себе
		// не останавливает все запросы. Liveness остаётся доступной до закрытия transport.
		if s.draining.Load() && r.URL.Path != "/health/live" {
			WriteError(w, r, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Service is draining")
			return
		}
		s.mux.ServeHTTP(w, r)
	})
	s.http = &http.Server{
		Handler:           middleware(logger, cfg.MaxBodyBytes, admission),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
		ErrorLog:          log.New(transportLog{logger}, "", 0),
	}
	return s
}

// Handle registers a transport route before Run. The server cannot be reused.
func (s *Server) Handle(pattern string, handler http.Handler) { s.mux.Handle(pattern, handler) }

// Run owns listener and blocks until cancellation or a serving failure. Request
// contexts remain alive during graceful drain and are cancelled on timeout.
func (s *Server) Run(ctx context.Context, listener net.Listener) error {
	if ctx.Err() != nil {
		_ = listener.Close()
		return nil
	}
	requests, cancelRequests := context.WithCancel(context.Background())
	// Независимый корень сохраняет активные requests при отмене signal context.
	// Deferred cancel освобождает их и после успешного завершения drain.
	defer cancelRequests()
	s.http.BaseContext = func(net.Listener) context.Context { return requests }
	served := make(chan error, 1)
	// Буфер позволяет Serve завершиться, пока основная goroutine находится в Shutdown.
	go func() { served <- s.http.Serve(listener) }()
	s.logger.Info("http server started", "address", listener.Addr().String())
	select {
	case err := <-served:
		s.draining.Store(true)
		cancelRequests()
		_ = s.http.Close()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	case <-ctx.Done():
	}
	s.draining.Store(true)
	s.http.SetKeepAlivesEnabled(false)
	s.logger.Info("http shutdown started", "timeout_ms", s.shutdownTimeout.Milliseconds())
	drain, cancelDrain := context.WithTimeout(context.Background(), s.shutdownTimeout)
	// ctx уже отменён сигналом; использовать его здесь означало бы пропустить grace period.
	err := s.http.Shutdown(drain)
	cancelDrain()
	if err != nil {
		// Shutdown не закрывает активные соединения по своему deadline. Отменяем
		// handlers и закрываем transport явно, затем дожидаемся завершения Serve.
		cancelRequests()
		_ = s.http.Close()
		<-served
		s.logger.Error("http shutdown forced", "error_code", "SHUTDOWN_TIMEOUT")
		return fmt.Errorf("shutdown HTTP: %w", err)
	}
	<-served
	s.logger.Info("http shutdown completed")
	return nil
}

// readOnly возвращает единый JSON 405 и Allow для probes. Path-only registration
// позволяет сохранить этот envelope вместо стандартного текстового ответа router.
func readOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			WriteError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
			return
		}
		next(w, r)
	}
}

// transportLog адаптирует внутренний logger net/http, отбрасывая потенциально
// чувствительные свободные сообщения; сохраняется только факт transport error.
type transportLog struct{ logger *slog.Logger }

// Write сообщает log.Logger, что сообщение принято полностью, не сохраняя его содержимое.
func (l transportLog) Write(data []byte) (int, error) {
	// net/http's free-form messages are not safe application log fields.
	l.logger.Error("http transport error", "error_code", "HTTP_TRANSPORT_ERROR")
	return len(data), nil
}
