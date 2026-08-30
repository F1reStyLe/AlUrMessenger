package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestRemoteContract exercises real HTTP boundaries, including responses that must
// never turn an Auth outage or malformed success into an authenticated Chat actor.
func TestRemoteContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"identity-only", 200, `{"id":42,"role":"admin","roles":["admin"],"project_id":"untrusted"}`, nil},
		{"revoked", 401, `{}`, ErrUnauthenticated},
		{"blocked", 403, `{}`, ErrUnauthenticated},
		{"outage", 503, `{}`, ErrUnavailable},
		{"limited", 429, `{}`, ErrUnavailable},
		{"malformed", 200, `{`, ErrUnavailable},
		{"missing-id", 200, `{"role":"admin"}`, ErrUnavailable},
		{"zero-id", 200, `{"id":0}`, ErrUnavailable},
		{"negative-id", 200, `{"id":-1}`, ErrUnavailable},
		{"string-id", 200, `{"id":"42"}`, ErrUnavailable},
		{"fraction-id", 200, `{"id":1.5}`, ErrUnavailable},
		{"trailing-data", 200, `{"id":42}{}`, ErrUnavailable},
		{"oversized", 200, `{"id":42,"unused":"` + strings.Repeat("x", 16384) + `"}`, ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/v1/users/me" || r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("incorrect Auth request")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			p := Project{ID: uuid.NewString(), Active: true}
			v, err := NewRemote(server.URL, p.ID, "test", projectFixture{p})
			if err != nil {
				t.Fatal(err)
			}
			defer v.client.CloseIdleConnections()
			id, err := v.Verify(t.Context(), "fixture-token")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
			if err == nil && (id.ExternalID != "42" || id.ProjectID != p.ID || id.Admin) {
				t.Fatal("Auth role or Project leaked into Chat")
			}
		})
	}
}

// TestRemoteRevocationAndBounds proves there is no positive cache and no requests
// with invalid headers, oversized tokens, or an inactive Project.
func TestRemoteRevocationAndBounds(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"id":42}`)
		} else {
			w.WriteHeader(401)
		}
	}))
	defer server.Close()
	p := Project{ID: uuid.NewString(), Active: true}
	v, err := NewRemote(server.URL, p.ID, "test", projectFixture{p})
	if err != nil {
		t.Fatal(err)
	}
	defer v.client.CloseIdleConnections()
	if _, err = v.Verify(t.Context(), "same-token"); err != nil {
		t.Fatal(err)
	}
	if _, err = v.Verify(t.Context(), "same-token"); err != ErrUnauthenticated {
		t.Fatal("revoked identity was cached")
	}
	for _, token := range []string{"", "header\r\ninjection", strings.Repeat("x", 8193)} {
		if _, err = v.Verify(t.Context(), token); err != ErrUnauthenticated {
			t.Fatal("invalid token accepted")
		}
	}
	p.Active = false
	if _, err = v.WithProjects(projectFixture{p}).Verify(t.Context(), "same-token"); err != ErrUnauthenticated {
		t.Fatal("inactive Project accepted")
	}
	if calls.Load() != 2 {
		t.Fatal("invalid credentials reached Auth")
	}
	if _, err = v.WithProjects(nil).Verify(t.Context(), "same-token"); err != ErrUnavailable {
		t.Fatal("missing store admitted")
	}
}

// TestRemoteRedirectAndTimeout prevents credential forwarding to another host and
// keeps unavailable upstream requests bounded, including caller cancellation.
func TestRemoteRedirectAndTimeout(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer source.Close()
	p := Project{ID: uuid.NewString(), Active: true}
	v, err := NewRemote(source.URL, p.ID, "test", projectFixture{p})
	if err != nil {
		t.Fatal(err)
	}
	defer v.client.CloseIdleConnections()
	if _, err = v.Verify(t.Context(), "fixture-token"); err != ErrUnavailable || leaked.Load() {
		t.Fatal("redirect followed")
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	v, err = NewRemote(slow.URL, p.ID, "test", projectFixture{p})
	if err != nil {
		t.Fatal(err)
	}
	defer v.client.CloseIdleConnections()
	v.client.Timeout = 30 * time.Millisecond
	if _, err = v.Verify(t.Context(), "fixture-token"); err != ErrUnavailable {
		t.Fatal("timeout not reported")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = v.Verify(ctx, "fixture-token"); err != ErrUnavailable {
		t.Fatal("cancellation not reported")
	}
}

// TestRemoteConfiguration checks the trusted endpoint before any socket is bound.
func TestRemoteConfiguration(t *testing.T) {
	for _, base := range []string{"", "://bad", "http://auth.example", "ftp://auth.example", "https://user:secret@auth.example", "https://auth.example/path", "https://auth.example?key=secret", "https://auth.example?", "https://auth.example#secret"} {
		if _, err := NewRemote(base, uuid.NewString(), "production", nil); err == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
	if _, err := NewRemote("https://auth.example", uuid.NewString(), "production", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRemote("https://auth.example", "", "production", nil); err == nil {
		t.Fatal("missing Project accepted")
	}
}
