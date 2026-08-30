package apidocs

import (
	"bytes"
	"github.com/F1reStyLe/AlUrMessenger/api/openapi"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestLocalDocumentation checks the served contract, MIME/CSP and exact asset allowlist.
func TestLocalDocumentation(t *testing.T) {
	for path, kind := range map[string]string{"/docs/api": "text/html", "/docs/api/": "text/html", "/docs/api/openapi.json": "application/json", "/docs/api/swagger-ui-bundle.js": "text/javascript", "/docs/api/initializer.js": "text/javascript", "/docs/api/swagger-ui.css": "text/css"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), kind) {
				t.Fatal("documentation unavailable", w.Code)
			}
			if !strings.Contains(w.Header().Get("Content-Security-Policy"), "connect-src 'self'") {
				t.Fatal("external network not restricted")
			}
			if path == "/docs/api/openapi.json" && !bytes.Equal(w.Body.Bytes(), openapi.Document) {
				t.Fatal("served spec differs from reviewed spec")
			}
			head := httptest.NewRecorder()
			Handler().ServeHTTP(head, httptest.NewRequest("HEAD", path, nil))
			if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(w.Body.Len()) {
				t.Fatal("HEAD contract invalid")
			}
		})
	}
	for _, path := range []string{"/docs/api/assets", "/docs/api/LICENSE", "/docs/api/unknown", "/docs/api/../../Agents.md"} {
		w := httptest.NewRecorder()
		Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatal("unexpected asset served")
		}
	}
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest("POST", "/docs/api", nil))
	if w.Code != 405 || w.Header().Get("Allow") != "GET, HEAD" {
		t.Fatal("unsafe method accepted")
	}
}
