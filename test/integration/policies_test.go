//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"github.com/F1reStyLe/AlUrMessenger/internal/auth"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	identityrepo "github.com/F1reStyLe/AlUrMessenger/internal/identity/repository"
	identityhttp "github.com/F1reStyLe/AlUrMessenger/internal/identity/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/admission"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/httpserver"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	policyrepo "github.com/F1reStyLe/AlUrMessenger/internal/policy/repository"
	policyhttp "github.com/F1reStyLe/AlUrMessenger/internal/policy/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/provision"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// testPolicies exercises HTTP permission checks and atomic SQL writes with real Redis.
func testPolicies(t *testing.T, db *pgxpool.Pool, operator *pgx.Conn, redisURL string) {
	p := provision.Project{ID: uuid.NewString(), Name: "Policy A", Issuer: "https://issuer.test", Audience: "chat"}
	q := p
	q.ID = uuid.NewString()
	q.Name = "Policy B"
	for _, project := range []provision.Project{p, q} {
		if err := provision.Create(t.Context(), operator, project); err != nil {
			t.Fatal(err)
		}
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	store := &identityrepo.Store{DB: db}
	v, err := auth.New(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: raw}), store)
	if err != nil {
		t.Fatal(err)
	}
	identities := &identity.Service{Verifier: v, Store: store}
	sign := func(project, subject, role string) string {
		c := auth.Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: p.Issuer, Audience: jwt.ClaimStrings{p.Audience}, Subject: subject, ExpiresAt: jwt.NewNumericDate(time.Now().Add(2 * time.Minute))}, ProjectID: project, Roles: []string{role}, TokenUse: "access"}
		s, e := jwt.NewWithClaims(jwt.SigningMethodRS256, c).SignedString(key)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	adminToken, userToken, otherToken := sign(p.ID, "admin", "admin"), sign(p.ID, "user", "user"), sign(q.ID, "admin", "admin")
	admin, err := identities.Authenticate(t.Context(), adminToken)
	if err != nil {
		t.Fatal(err)
	}
	if admin.Admin {
		t.Fatal("JWT role granted admin before operator assignment")
	}
	if _, err = identities.Authenticate(t.Context(), otherToken); err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{p.ID, q.ID} {
		if err = provision.SetRole(t.Context(), operator, provision.RoleAssignment{ProjectID: project, ExternalID: "admin", Role: "admin"}); err != nil {
			t.Fatal(err)
		}
	}
	admin, err = identities.Authenticate(t.Context(), adminToken)
	if err != nil || !admin.Admin {
		t.Fatal("local admin not resolved")
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	options.ContextTimeoutEnabled = true
	cache := redis.NewClient(options)
	defer cache.Close()
	limiter := &admission.Limiter{Redis: cache, Config: admission.Config{Origins: map[string]bool{"https://demo.test": true}, IPPerMinute: 1000, UserPerMinute: 1000}}
	policies := &policy.Service{Store: &policyrepo.Store{DB: db}}
	protect := func(next http.Handler) http.Handler {
		return limiter.Before(identityhttp.Authenticate(identities, limiter.After(next)))
	}
	server := httpserver.New(config.HTTP{MaxBodyBytes: 4096, ReadHeaderTimeout: time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context) error { return nil })
	identityhttp.Register(server, identities, protect)
	policyhttp.Register(server, policies, protect)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, listener) }()
	client := &http.Client{Timeout: 5 * time.Second}
	defer func() {
		client.CloseIdleConnections()
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	request := func(method, path, token, body, origin string) (int, []byte, http.Header) {
		t.Helper()
		req, e := http.NewRequest(method, "http://"+listener.Addr().String()+path, strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if method == "OPTIONS" {
			req.Header.Set("Access-Control-Request-Method", "PATCH")
			req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
		}
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		data, e := io.ReadAll(res.Body)
		if e != nil {
			t.Fatal(e)
		}
		return res.StatusCode, data, res.Header
	}
	if code, _, _ := request("GET", "/admin/v1/project", userToken, "", ""); code != 403 {
		t.Fatal("user has admin access", code)
	}
	if code, _, _ := request("GET", "/admin/v1/project", sign(p.ID, "user", "admin"), "", ""); code != 403 {
		t.Fatal("signed external admin bypassed local role", code)
	}
	if code, _, _ := request("GET", "/admin/v1/project", "", "", ""); code != 401 {
		t.Fatal("anonymous admin", code)
	}
	if code, _, h := request("OPTIONS", "/admin/v1/project", "", "", "https://demo.test"); code != 204 || h.Get("Access-Control-Allow-Origin") != "https://demo.test" {
		t.Fatal("valid preflight denied", code)
	}
	if code, _, h := request("GET", "/admin/v1/project", adminToken, "", "https://evil.test"); code != 403 || h.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unknown origin allowed", code)
	}
	if code, data, _ := request("GET", "/admin/v1/project", adminToken, "", ""); code != 200 || !strings.Contains(string(data), `"settings_version":"1"`) {
		t.Fatal("defaults unavailable", code)
	}
	for _, body := range []string{`{"expected_version":"1","flags":{"unknown":true}}`, `{"expected_version":"1","flags":{"allow_images":null}}`, `{"expected_version":"1","max_upload_size":52428801}`, `{"expected_version":"1","message_retention_days":0}`, `{"expected_version":"1","project_id":"` + q.ID + `","flags":{"allow_images":false}}`} {
		if code, _, _ := request("PATCH", "/admin/v1/project", adminToken, body, ""); code != 400 {
			t.Fatal("invalid patch accepted", code)
		}
	}
	// Concurrent same-version writes: exactly one commit and one audit entry.
	patch := policy.Patch{ExpectedVersion: 1, Flags: map[string]*bool{}}
	disabled := false
	patch.Flags["allow_images"] = &disabled
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := policies.Update(t.Context(), admin, patch, policy.Trace{RequestID: uuid.NewString(), IP: "127.0.0.1"})
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if e == policy.ErrConflict {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("lost update")
	}
	if code, _, _ := request("PATCH", "/admin/v1/feature-flags", adminToken, `{"expected_version":"1","flags":{"allow_images":true}}`, ""); code != 409 {
		t.Fatal("stale update accepted", code)
	}
	if code, _, _ := request("PATCH", "/admin/v1/feature-flags", adminToken, `{"expected_version":"2","flags":{"allow_reactions":false}}`, ""); code != 200 {
		t.Fatal("HTTP flag patch failed", code)
	}
	other, err := policies.Store.Get(t.Context(), q.ID)
	if err != nil || !other.Flags["allow_images"] || other.Version != 1 {
		t.Fatal("settings crossed Project")
	}
	if code, data, _ := request("GET", "/admin/v1/audit-logs", adminToken, "", ""); code != 200 {
		t.Fatal("audit unavailable")
	} else {
		var page struct {
			Items []policy.Audit `json:"items"`
		}
		if json.Unmarshal(data, &page) != nil || len(page.Items) != 2 {
			t.Fatal("audit not atomic")
		}
	}
	if code, data, _ := request("GET", "/admin/v1/audit-logs", otherToken, "", ""); code != 200 || !strings.Contains(string(data), `"items":[]`) {
		t.Fatal("cross-project audit leak")
	}
	if _, err = db.Exec(t.Context(), "UPDATE chat.audit_logs SET action='tampered' WHERE project_id=$1", p.ID); err == nil {
		t.Fatal("audit mutable")
	}
	if _, err = db.Exec(t.Context(), "DELETE FROM chat.audit_logs WHERE project_id=$1", p.ID); err == nil {
		t.Fatal("audit deletable")
	}
	// Invalid audit trace fails INSERT after settings UPDATE; rollback must restore version/flag.
	patch.ExpectedVersion = 3
	enabled := true
	patch.Flags["allow_images"] = &enabled
	if _, err = policies.Update(t.Context(), admin, patch, policy.Trace{RequestID: "invalid"}); err == nil {
		t.Fatal("invalid audit committed")
	}
	current, err := policies.Store.Get(t.Context(), p.ID)
	if err != nil || current.Version != 3 || current.Flags["allow_images"] {
		t.Fatal("audit failure did not rollback mutation")
	}
	// The old token immediately loses privileges, and a previously built Actor
	// cannot bypass demotion when a policy transaction rechecks its current role.
	assignment := provision.RoleAssignment{ProjectID: p.ID, ExternalID: "admin", Role: "user"}
	if err = provision.SetRole(t.Context(), operator, assignment); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := request("GET", "/admin/v1/project", adminToken, "", ""); code != 403 {
		t.Fatal("demotion requires new JWT", code)
	}
	if _, err = policies.Update(t.Context(), admin, patch, policy.Trace{RequestID: uuid.NewString()}); err != policy.ErrForbidden {
		t.Fatal("stale actor bypassed role check", err)
	}
	assignment.Role = "admin"
	if err = provision.SetRole(t.Context(), operator, assignment); err != nil {
		t.Fatal(err)
	}
	if _, err = operator.Exec(t.Context(), "UPDATE chat.users SET banned_at=now() WHERE project_id=$1 AND id=$2", p.ID, admin.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = policies.Update(t.Context(), admin, patch, policy.Trace{RequestID: uuid.NewString()}); err != identity.ErrBanned {
		t.Fatal("stale actor bypassed ban")
	}
	// Independent limiter instances share atomic counters; TTL makes all test keys temporary.
	small := &admission.Limiter{Redis: cache}
	keyName := "test:" + uuid.NewString()
	allowed := make(chan bool, 20)
	errors := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, retry, e := small.Take(t.Context(), keyName, 5)
			if e == nil && (retry < 1 || retry > 60) {
				e = policy.ErrInvalid
			}
			allowed <- ok
			errors <- e
		}()
	}
	wg.Wait()
	close(allowed)
	close(errors)
	count := 0
	for ok := range allowed {
		if ok {
			count++
		}
	}
	for e := range errors {
		if e != nil {
			t.Fatal(e)
		}
	}
	if count != 5 {
		t.Fatal("non-atomic limiter", count)
	}
	// Per-user rejection is distinguishable from IP quota, and returns Retry-After.
	limiter.Config.UserPerMinute = 1
	if code, _, h := request("GET", "/api/v1/me", userToken, "", ""); code != 429 || h.Get("Retry-After") == "" {
		t.Fatal("user limiter missing", code)
	}
	limiter.Config.UserPerMinute = 1000
	limiter.Config.IPPerMinute = 1
	if code, _, h := request("GET", "/api/v1/me", otherToken, "", ""); code != 429 || h.Get("Retry-After") == "" {
		t.Fatal("IP limiter missing", code)
	}
	if err = cache.Close(); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := request("GET", "/api/v1/me", otherToken, "", ""); code != 503 {
		t.Fatal("Redis failure admitted request", code)
	}
}
