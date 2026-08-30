package httpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// requestIDKey не экспортируется, чтобы другие пакеты не перезаписали значение
// через совпавший строковый ключ context.
type requestIDKey struct{}

// RequestID возвращает проверенный middleware ID; вне HTTP context возвращает пустую строку.
// Этот ID служит корреляции, а не авторизации или дедупликации бизнес-операций.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// newRequestID создаёт UUID v4 из криптографически случайных байтов.
// Биты версии/варианта выставляются явно; идентификатор не содержит пользовательских данных.
func newRequestID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic("request ID generation failed")
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	h := hex.EncodeToString(id[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// validRequestID ожидает lowercase UUID: canonical layout, версии 1–8, RFC variant.
// Проверка ограничивает формат и длину недоверенного значения до попадания в логи.
func validRequestID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	if id[14] < '1' || id[14] > '8' || !strings.ContainsRune("89ab", rune(id[19])) {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil
}

// responseRecorder фиксирует окончательный статус и реально записанный объём ответа.
// Используется одной goroutine запроса; параллельная запись в ResponseWriter не поддерживается.
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

// Unwrap позволяет http.ResponseController обращаться к возможностям исходного writer.
func (w *responseRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// WriteHeader сохраняет первый окончательный статус; промежуточные 1xx не фиксируют ответ.
func (w *responseRecorder) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	if status >= 200 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

// Write учитывает неявный 200 при записи без WriteHeader и только фактически принятые байты.
func (w *responseRecorder) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	return n, err
}

// middleware применяет общие гарантии даже к неизвестным маршрутам: correlation ID,
// безопасные headers/logs, recovery и ограничение body перед передачей управления router.
func middleware(logger *slog.Logger, maxBody int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.ToLower(r.Header.Get("X-Request-ID"))
		if !validRequestID(id) {
			id = newRequestID()
		}
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		rec := &responseRecorder{ResponseWriter: w}
		started := time.Now()
		// Defer выполняются в обратном порядке: recovery сначала формирует ошибку,
		// затем access log видит итоговый статус. Сырые значения запроса не логируются.
		defer func() {
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			method := r.Method
			switch method {
			case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE":
			default:
				method = "OTHER"
			}
			// Log the registered pattern, never a raw path, query, header or body.
			logger.InfoContext(r.Context(), "http request completed", "request_id", id,
				"method", method, "route", r.Pattern, "status", status,
				"duration_ms", time.Since(started).Milliseconds(), "bytes", rec.bytes)
		}()
		defer func() {
			if recovered := recover(); recovered != nil {
				// Panic values/stack traces can contain user data or secrets.
				logger.ErrorContext(r.Context(), "http request aborted", "request_id", id, "error_code", "HANDLER_PANIC")
				if recovered == http.ErrAbortHandler || rec.status != 0 {
					// Уже начатый ответ нельзя заменить JSON-ошибкой: просим net/http
					// прервать поток без вывода исходного panic и stack trace.
					panic(http.ErrAbortHandler)
				}
				WriteError(rec, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error")
			}
		}()
		if r.ContentLength > maxBody {
			WriteError(rec, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Request body exceeds the configured limit")
			return
		}
		if r.Body != nil {
			// Content-Length недостаточно для chunked body. Обёртка проверяет байты
			// при чтении handler; probes тело не читают и JSON payload не требуют.
			r.Body = http.MaxBytesReader(rec, r.Body, maxBody)
		}
		next.ServeHTTP(rec, r)
	})
}
