package httpserver

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPatchNull distinguishes omitted optional fields, explicit clear and invalid null.
func TestPatchNull(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{{`{"name":"Alice"}`, true}, {`{"name":""}`, true}, {`{"name":null}`, false}, {`{"name":"Alice","other":null}`, false}} {
		t.Run(tc.body, func(t *testing.T) {
			r := httptest.NewRequest("PATCH", "/", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			var dto struct {
				Name *string `json:"name"`
			}
			if ReadPatchJSON(w, r, &dto) != tc.valid {
				t.Fatal("patch null contract mismatch")
			}
			if !tc.valid && w.Code != 400 {
				t.Fatal("expected bad request")
			}
		})
	}
}
