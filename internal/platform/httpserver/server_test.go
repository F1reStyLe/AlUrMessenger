package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
)

// testServer: Создаёт transport с малыми лимитами и подставным readiness; реальные зависимости не нужны.
func testServer(ready Readiness, output io.Writer) *Server {
	return New(config.HTTP{
		ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second,
		WriteTimeout: 2 * time.Second, IdleTimeout: time.Second,
		MaxHeaderBytes: 32768, MaxBodyBytes: 1024,
	}, time.Second, slog.New(slog.NewJSONHandler(output, nil)), ready)
}

// errorCode: Проверяет JSON envelope и совпадение request ID в теле/заголовке перед возвратом кода ошибки.
func errorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var body errorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("non-JSON error response: %s (%v)", response.Body.String(), err)
	}
	if body.Error.RequestID != response.Header().Get("X-Request-ID") || !validRequestID(body.Error.RequestID) {
		t.Fatalf("error and header must share a valid request ID: %+v", body)
	}
	return body.Error.Code
}

// TestProbeSemantics: Сверяет статусы probes, fallback и методы; успешный callback проверяет adapter, не текущий bootstrap.
func TestProbeSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		ready              Readiness
		status             int
		code               string
	}{
		{"live without dependencies", "GET", "/health/live", nil, 200, ""},
		{"unconfigured is not ready", "GET", "/health/ready", nil, 503, "DEPENDENCY_UNAVAILABLE"},
		{"failed dependency", "GET", "/health/ready", func(context.Context) error { return errors.New("secret-DSN") }, 503, "DEPENDENCY_UNAVAILABLE"},
		{"healthy dependency", "GET", "/health/ready", func(context.Context) error { return nil }, 200, ""},
		{"unknown route", "GET", "/api/v1/messages", nil, 404, "RESOURCE_NOT_FOUND"},
		{"unsupported method", "POST", "/health/live", nil, 405, "METHOD_NOT_ALLOWED"},
		{"HEAD has no body", "HEAD", "/health/live", nil, 200, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(tc.ready, io.Discard)
			rr := httptest.NewRecorder()
			s.http.Handler.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, nil))
			if rr.Code != tc.status {
				t.Fatalf("status=%d, want %d", rr.Code, tc.status)
			}
			if tc.code != "" && errorCode(t, rr) != tc.code {
				t.Fatalf("unexpected error: %s", rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "secret-DSN") {
				t.Fatal("dependency details leaked")
			}
			if tc.status == 405 && rr.Header().Get("Allow") != "GET, HEAD" {
				t.Fatal("missing Allow header")
			}
			if tc.method == "HEAD" && rr.Body.Len() != 0 {
				t.Fatal("HEAD returned a response body")
			}
		})
	}
}

// TestReadinessIsRecheckedAfterDependencyCheck: Удерживает callback каналом, чтобы гарантированно начать drain до возврата успешной проверки.
func TestReadinessIsRecheckedAfterDependencyCheck(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	s := testServer(func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, io.Discard)
	rr := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.http.Handler.ServeHTTP(rr, httptest.NewRequest("GET", "/health/ready", nil))
		close(done)
	}()
	receive(t, entered)
	s.draining.Store(true)
	close(release)
	receive(t, done)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatal("dependency success must not override a drain started during the check")
	}
}

// TestRequestIDsAndSecretSafeLogs: Размещает canary в разных недоверенных полях и panic, чтобы выявить утечку в ответ или лог.
func TestRequestIDsAndSecretSafeLogs(t *testing.T) {
	const canary = "sensitive-token-canary"
	var logs bytes.Buffer
	s := testServer(nil, &logs)
	s.Handle("/panic", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(canary) }))
	for _, path := range []string{"/panic", "/" + canary} {
		request := httptest.NewRequest("POST", path+"?access_token="+canary, strings.NewReader(canary))
		request.Header.Set("Authorization", "Bearer "+canary)
		request.Header.Set("X-API-Key", canary)
		request.Header.Set("X-Request-ID", canary)
		rr := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(rr, request)
		if path == "/panic" && errorCode(t, rr) != "INTERNAL_ERROR" {
			t.Fatalf("panic did not become a safe error: %s", rr.Body.String())
		}
		if strings.Contains(logs.String(), canary) || strings.Contains(rr.Body.String(), canary) {
			t.Fatal("request or panic secret leaked into logs/response")
		}
		if !validRequestID(rr.Header().Get("X-Request-ID")) {
			t.Fatal("invalid incoming request ID was not replaced")
		}
	}
	const validID = "01900000-0000-4000-8000-000000000001"
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/health/live", nil)
	r.Header.Set("X-Request-ID", strings.ToUpper(validID))
	s.http.Handler.ServeHTTP(rr, r)
	if rr.Header().Get("X-Request-ID") != validID || !strings.Contains(logs.String(), validID) {
		t.Fatal("valid request ID was not propagated to response and logs")
	}
}

// TestJSONValidationAndStreamingBounds: Проверяет DTO boundary через тестовый /input, включая неизвестную длину и пробелы после JSON.
func TestJSONValidationAndStreamingBounds(t *testing.T) {
	for _, tc := range []struct {
		name, body, contentType string
		status                  int
		stream                  bool
	}{
		{"valid", `{"name":"ok"}`, "application/json", 200, false},
		{"null", `null`, "application/json", 400, false},
		{"array", `[]`, "application/json", 400, false},
		{"unknown field", `{"secret":"value"}`, "application/json", 400, false},
		{"two objects", `{} {}`, "application/json", 400, false},
		{"malformed", `{`, "application/json", 400, false},
		{"empty", ``, "application/json", 400, false},
		{"media type", `{}`, "text/plain", 415, false},
		{"known length too large", `{"name":"` + strings.Repeat("x", 2048) + `"}`, "application/json", 413, false},
		{"stream too large", `{"name":"` + strings.Repeat("x", 2048) + `"}`, "application/json", 413, true},
		{"oversized trailing whitespace", `{}` + strings.Repeat(" ", 2048), "application/json", 413, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(nil, io.Discard)
			processed := false
			s.Handle("/input", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Name string `json:"name"`
				}
				if ReadJSON(w, r, &body) {
					processed = true
					WriteJSON(w, r, http.StatusOK, body)
				}
			}))
			request := httptest.NewRequest("POST", "/input", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", tc.contentType)
			if tc.stream {
				request.ContentLength = -1
			}
			rr := httptest.NewRecorder()
			s.http.Handler.ServeHTTP(rr, request)
			if rr.Code != tc.status || processed != (tc.status == 200) {
				t.Fatalf("status=%d processed=%v, want status=%d: %s", rr.Code, processed, tc.status, rr.Body.String())
			}
			if tc.status != 200 {
				_ = errorCode(t, rr)
			}
		})
	}
}

// TestPanicAfterHeadersAbortsInsteadOfAppendingJSON: Фиксирует контракт частичного ответа: abort вместо дописывания JSON после уже отправленных байтов.
func TestPanicAfterHeadersAbortsInsteadOfAppendingJSON(t *testing.T) {
	s := testServer(nil, io.Discard)
	s.Handle("/partial", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "partial")
		panic("secret")
	}))
	rr := httptest.NewRecorder()
	defer func() {
		if recover() != http.ErrAbortHandler {
			t.Error("partial response must abort its connection")
		}
		if rr.Body.String() != "partial" {
			t.Error("error JSON must not be appended to a committed response")
		}
	}()
	s.http.Handler.ServeHTTP(rr, httptest.NewRequest("GET", "/partial", nil))
}
