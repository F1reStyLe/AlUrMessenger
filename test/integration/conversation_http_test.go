//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/internal/auth"
	"github.com/F1reStyLe/AlUrMessenger/internal/conversation"
	conversationrepo "github.com/F1reStyLe/AlUrMessenger/internal/conversation/repository"
	conversationhttp "github.com/F1reStyLe/AlUrMessenger/internal/conversation/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	identityrepo "github.com/F1reStyLe/AlUrMessenger/internal/identity/repository"
	identityhttp "github.com/F1reStyLe/AlUrMessenger/internal/identity/transport"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/admission"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/httpserver"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// fixtureVerifier replaces only external Auth networking; identity, roles, Redis,
// transport, transactions and SQL constraints are real in this HTTP integration test.
type fixtureVerifier map[string]auth.Identity

func (v fixtureVerifier) Verify(_ context.Context, raw string) (auth.Identity, error) {
	a, ok := v[raw]
	if !ok {
		return auth.Identity{}, auth.ErrUnauthenticated
	}
	return a, nil
}

// testConversationHTTP checks request/response contracts and middleware composition,
// including preflight, scoped cursors, unknown fields and permissions after leave.
func testConversationHTTP(t *testing.T, db *pgxpool.Pool, redisURL string, actors []identity.Actor) {
	verifier := fixtureVerifier{}
	for i, token := range []string{"a", "b", "outsider", "foreign"} {
		verifier[token] = auth.Identity{ProjectID: actors[i].ProjectID, ExternalID: actors[i].User.ExternalID}
	}
	ids := &identity.Service{Verifier: verifier, Store: &identityrepo.Store{DB: db}}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	cache := redis.NewClient(opts)
	defer cache.Close()
	limiter := &admission.Limiter{Redis: cache, Config: admission.Config{IPPerMinute: 1000, UserPerMinute: 1000, Origins: map[string]bool{"https://demo.test": true}}}
	protect := func(next http.Handler) http.Handler {
		return limiter.Before(identityhttp.Authenticate(ids, limiter.After(next)))
	}
	server := httpserver.New(config.HTTP{ReadHeaderTimeout: time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, MaxBodyBytes: 4096}, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context) error { return nil })
	store := &conversationrepo.Store{DB: db}
	conversationhttp.Register(server, &conversation.Service{Store: store}, protect)
	conversationhttp.RegisterMembership(server, &conversation.MembershipService{Store: store}, protect)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
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
	request := func(method, path, token, body string, want int) []byte {
		t.Helper()
		req, err := http.NewRequest(method, "http://"+listener.Addr().String()+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if method == "OPTIONS" {
			req.Header.Set("Origin", "https://demo.test")
			req.Header.Set("Access-Control-Request-Method", body)
			req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			t.Fatalf("%s %s got=%d want=%d body=%s", method, path, resp.StatusCode, want, string(data))
		}
		return data
	}
	collection := "/api/v1/conversations"
	request("GET", collection, "", "", 401)
	request("POST", collection, "a", `{"type":"GROUP","project_id":"injected"}`, 400)
	request("POST", collection, "a", `{"type":"GROUP","title":null}`, 400)
	data := request("POST", collection, "a", `{"type":"GROUP","title":"HTTP","member_ids":["`+actors[1].User.ID+`"]}`, 201)
	var c conversation.Conversation
	if json.Unmarshal(data, &c) != nil || c.Membership.Role != "moderator" {
		t.Fatal("invalid create DTO")
	}
	path := collection + "/" + c.ID
	request("GET", path, "outsider", "", 404)
	request("GET", path, "foreign", "", 404)
	request("PATCH", path, "b", `{"title":"Denied","expected_version":"1"}`, 403)
	request("PATCH", path, "a", `{"title":"Updated","expected_version":"1"}`, 200)
	request("PATCH", path, "a", `{"title":"Stale","expected_version":"1"}`, 409)
	request("OPTIONS", collection, "", "POST", 204)
	request("OPTIONS", path, "", "PATCH", 204)
	data = request("GET", collection+"?limit=1", "a", "", 200)
	var page struct {
		Next *string `json:"next_cursor"`
	}
	if json.Unmarshal(data, &page) != nil || page.Next == nil {
		t.Fatal("cursor missing")
	}
	request("GET", collection+"?cursor="+*page.Next, "a", "", 200)
	request("GET", collection+"?cursor="+*page.Next, "b", "", 400)
	request("GET", collection+"?limit=101", "a", "", 400)
	request("POST", path+"/members", "a", `{"user_ids":["`+actors[2].User.ID+`"]}`, 200)
	request("GET", path+"/members", "b", "", 200)
	request("GET", path+"/members?cursor="+*page.Next, "a", "", 400)
	request("PATCH", path+"/members/"+actors[1].User.ID, "a", `{"role":"moderator","expected_version":"1"}`, 200)
	request("OPTIONS", path+"/members/"+actors[0].User.ID, "", "DELETE", 204)
	request("DELETE", path+"/members/"+actors[0].User.ID, "a", "", 204)
	request("GET", path, "a", "", 404)
	request("PATCH", path, "a", `{"title":"Left","expected_version":"2"}`, 404)
	request("GET", path, "b", "", 200)
}
