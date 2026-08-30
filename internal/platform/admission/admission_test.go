package admission

import (
	"net/http/httptest"
	"testing"
)

// TestOriginConfiguration rejects wildcard, credentials, paths and insecure production.
func TestOriginConfiguration(t *testing.T) {
	for _, origin := range []string{"*", "https://*.example.com", "https://user:pass@example.com", "https://example.com/path", "https://example.com/", "null", "http://example.com"} {
		t.Run(origin, func(t *testing.T) {
			t.Setenv("CORS_ALLOWED_ORIGINS", origin)
			if _, err := Load("production"); err == nil {
				t.Fatal("unsafe origin accepted")
			}
		})
	}
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://demo.example.com")
	if _, err := Load("production"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RATE_IP_PER_MINUTE", "0")
	if _, err := Load("production"); err == nil {
		t.Fatal("limiter disabled")
	}
}

// TestPeerIP proves client forwarding headers cannot choose the rate-limit identity.
func TestPeerIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "[::ffff:192.0.2.1]:123"
	r.Header.Set("X-Forwarded-For", "203.0.113.2")
	r.Header.Set("Forwarded", "for=203.0.113.2")
	if PeerIP(r) != "192.0.2.1" {
		t.Fatal("forwarded address trusted")
	}
}
