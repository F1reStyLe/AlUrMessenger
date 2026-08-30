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
	"github.com/F1reStyLe/AlUrMessenger/internal/identity/repository"
	identityhttp "github.com/F1reStyLe/AlUrMessenger/internal/identity/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/httpserver"
	"github.com/F1reStyLe/AlUrMessenger/internal/provision"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// testIdentity runs against fresh migrated DB using the real restricted runtime role.
// It is nested after migrations and before dependency failure scenarios.
func testIdentity(t *testing.T, db *pgxpool.Pool, operator *pgx.Conn) {
	p := provision.Project{ID: uuid.NewString(), Name: "A", Issuer: "https://issuer.test", Audience: "chat"}
	q := p
	q.ID = uuid.NewString()
	q.Name = "B"
	for _, project := range []provision.Project{p, q} {
		if err := provision.Create(t.Context(), operator, project); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(t.Context(), "UPDATE chat.projects SET auth_issuer='https://evil' WHERE id=$1", p.ID); err == nil {
		t.Fatal("runtime can retarget trust binding")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	store := &repository.Store{DB: db}
	v, err := auth.New(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: raw}), store)
	if err != nil {
		t.Fatal(err)
	}
	sign := func(project, subject string) string {
		c := auth.Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: p.Issuer, Audience: jwt.ClaimStrings{p.Audience}, Subject: subject, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}, ProjectID: project, Roles: []string{"user"}, TokenUse: "access"}
		s, e := jwt.NewWithClaims(jwt.SigningMethodRS256, c).SignedString(key)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	service := &identity.Service{Verifier: v, Store: store}
	// Many requests for the same external identity must return exactly one internal ID.
	token := sign(p.ID, "same-external")
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); a, e := service.Authenticate(t.Context(), token); errs <- e; ids <- a.User.ID }()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var userID string
	for id := range ids {
		if userID != "" && id != userID {
			t.Fatal("duplicate identity")
		}
		userID = id
	}
	b, err := service.Authenticate(t.Context(), sign(q.ID, "same-external"))
	if err != nil || b.User.ID == userID {
		t.Fatal("projects share identity")
	}
	if _, err = store.GetUser(t.Context(), p.ID, b.User.ID); err != identity.ErrNotFound {
		t.Fatal("cross-project profile visible")
	}
	var before, after int
	if err = db.QueryRow(t.Context(), "SELECT count(*) FROM chat.users").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Authenticate(t.Context(), token+"invalid"); err == nil {
		t.Fatal("invalid JWT accepted")
	}
	if err = db.QueryRow(t.Context(), "SELECT count(*) FROM chat.users").Scan(&after); err != nil || before != after {
		t.Fatal("invalid JWT provisions user")
	}
	server := httpserver.New(config.HTTP{MaxBodyBytes: 4096, ReadHeaderTimeout: time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context) error { return nil })
	identityhttp.Register(server, service, nil)
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
	request := func(method, path, body string) (int, []byte) {
		t.Helper()
		req, e := http.NewRequest(method, "http://"+listener.Addr().String()+path, strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
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
		return res.StatusCode, data
	}
	if code, _ := request("PATCH", "/api/v1/me", `{"display_name":"Alice","user_id":"`+b.User.ID+`"}`); code != 400 {
		t.Fatal("actor injection accepted", code)
	}
	if code, _ := request("PATCH", "/api/v1/me", `{"display_name":"Alice"}`); code != 200 {
		t.Fatal("profile patch failed", code)
	}
	if code, data := request("GET", "/api/v1/me", ""); code != 200 || !strings.Contains(string(data), "Alice") {
		t.Fatal("profile not preserved")
	}
	if code, _ := request("GET", "/api/v1/users/"+b.User.ID, ""); code != 404 {
		t.Fatal("cross-project HTTP leak", code)
	}
	if code, data := request("GET", "/api/v1/users?limit=1", ""); code != 200 || strings.Contains(string(data), "external_user_id") {
		t.Fatal("public profile leak")
	} else {
		var page struct {
			Items []identity.User `json:"items"`
		}
		if json.Unmarshal(data, &page) != nil || len(page.Items) != 1 {
			t.Fatal("invalid page")
		}
	}
	if code, _ := request("GET", "/api/v1/users?limit=101", ""); code != 400 {
		t.Fatal("unbounded page")
	}
	if _, err = operator.Exec(t.Context(), "UPDATE chat.users SET banned_at=now() WHERE project_id=$1 AND id=$2", p.ID, userID); err != nil {
		t.Fatal(err)
	}
	if code, _ := request("PATCH", "/api/v1/me", `{"display_name":"Denied"}`); code != 403 {
		t.Fatal("banned write accepted", code)
	}
	if code, _ := request("GET", "/api/v1/me", ""); code != 200 {
		t.Fatal("banned read denied", code)
	}
}
