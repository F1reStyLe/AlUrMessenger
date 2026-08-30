package identity

import (
	"context"
	"testing"

	"github.com/F1reStyLe/AlUrMessenger/internal/auth"
)

// roleVerifier intentionally claims admin to exercise the service trust boundary,
// even if a future verifier accidentally starts copying external privileges.
type roleVerifier struct{}

func (roleVerifier) Verify(context.Context, string) (auth.Identity, error) {
	return auth.Identity{ProjectID: "project", ExternalID: "external", Admin: true}, nil
}

// roleStore changes the persisted role between requests with the very same token.
type roleStore struct {
	Store
	role string
}

func (s *roleStore) EnsureUser(context.Context, auth.Identity) (User, error) {
	return User{ID: "local", Role: s.role}, nil
}

// TestRolesAlwaysComeFromStore guards grants, demotions and fail-closed unknown roles.
func TestRolesAlwaysComeFromStore(t *testing.T) {
	store := &roleStore{}
	s := &Service{Verifier: roleVerifier{}, Store: store}
	for _, role := range []string{"user", "admin", "user", "moderator", ""} {
		store.role = role
		a, err := s.Authenticate(t.Context(), "unchanged-token")
		if err != nil || a.Admin != (role == "admin") {
			t.Fatal("external/cached role used", role)
		}
	}
}
