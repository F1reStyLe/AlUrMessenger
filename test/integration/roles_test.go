//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/F1reStyLe/AlUrMessenger/internal/auth"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity/repository"
	"github.com/F1reStyLe/AlUrMessenger/internal/provision"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testLocalRoles uses real SQL grants and two Projects for the same Auth user.
// The remote Auth is an HTTP fixture; full real-service login/logout is a separate smoke.
func testLocalRoles(t *testing.T, db *pgxpool.Pool, operator *pgx.Conn) {
	var revoked atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if revoked.Load() {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"id":42,"role":"admin","roles":["admin"]}`)
	}))
	defer server.Close()
	store := &repository.Store{DB: db}
	var services []*identity.Service
	projects := []string{uuid.NewString(), uuid.NewString()}
	for _, project := range projects {
		if err := provision.Create(t.Context(), operator, provision.Project{ID: project, Name: "Role fixture", Issuer: "https://fixture.example", Audience: "chat"}); err != nil {
			t.Fatal(err)
		}
		verifier, err := auth.NewRemote(server.URL, project, "test", store)
		if err != nil {
			t.Fatal(err)
		}
		s := &identity.Service{Verifier: verifier, Store: store}
		services = append(services, s)
		a, err := s.Authenticate(t.Context(), "same-token")
		if err != nil || a.Admin || a.User.Role != "user" || a.ExternalID != "42" {
			t.Fatal("new user copied Auth admin", err)
		}
	}
	assignment := provision.RoleAssignment{ProjectID: projects[0], ExternalID: "42", Role: "admin"}
	for range 2 {
		if err := provision.SetRole(t.Context(), operator, assignment); err != nil {
			t.Fatal(err)
		}
	}
	a, err := services[0].Authenticate(t.Context(), "same-token")
	if err != nil || !a.Admin {
		t.Fatal("operator role ignored", err)
	}
	b, err := services[1].Authenticate(t.Context(), "same-token")
	if err != nil || b.Admin || a.User.ID == b.User.ID {
		t.Fatal("role crossed Project", err)
	}
	// Table privileges must not permit self-promotion even below HTTP DTO validation.
	if _, err := db.Exec(t.Context(), "UPDATE chat.users SET role='admin' WHERE project_id=$1", projects[1]); err == nil {
		t.Fatal("runtime can grant admin")
	}
	if _, err := db.Exec(t.Context(), "INSERT INTO chat.users(id,project_id,external_user_id,display_name,role) VALUES($1,$2,'injected','injected','admin')", uuid.NewString(), projects[1]); err == nil {
		t.Fatal("runtime can insert admin")
	}
	assignment.ExternalID = "missing"
	if err := provision.SetRole(t.Context(), operator, assignment); err == nil {
		t.Fatal("operator invented an identity")
	}
	assignment.ExternalID, assignment.Role = "42", "user"
	if err := provision.SetRole(t.Context(), operator, assignment); err != nil {
		t.Fatal(err)
	}
	a, err = services[0].Authenticate(t.Context(), "same-token")
	if err != nil || a.Admin {
		t.Fatal("demotion cached", err)
	}
	revoked.Store(true)
	if _, err := services[0].Authenticate(t.Context(), "same-token"); err != auth.ErrUnauthenticated {
		t.Fatal("revoked Auth session accepted", err)
	}
	// Development fixtures get initial DB roles, but repeat seed cannot undo an
	// operator demotion or overwrite existing profile data.
	if err := provision.Seed(t.Context(), "development", operator); err != nil {
		t.Fatal(err)
	}
	var role string
	if err := db.QueryRow(t.Context(), "SELECT role FROM chat.users WHERE project_id=$1 AND external_user_id='admin'", provision.DevProject).Scan(&role); err != nil || role != "admin" {
		t.Fatal("new seed admin missing", err)
	}
	if err := provision.SetRole(t.Context(), operator, provision.RoleAssignment{ProjectID: provision.DevProject, ExternalID: "admin", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := provision.Seed(t.Context(), "development", operator); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), "SELECT role FROM chat.users WHERE project_id=$1 AND external_user_id='admin'", provision.DevProject).Scan(&role); err != nil || role != "user" {
		t.Fatal("seed restored privileges", err)
	}
}
