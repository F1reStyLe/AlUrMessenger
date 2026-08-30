package provision

import (
	"testing"
)

// TestDevelopmentGuard proves rejection before parsing secrets or touching a nil DB.
func TestDevelopmentGuard(t *testing.T) {
	for _, env := range []string{"", "test", "production"} {
		if _, err := Token(env, nil, "admin", true); err == nil {
			t.Fatal("token guard bypassed")
		}
		if err := Seed(t.Context(), env, nil); err == nil {
			t.Fatal("seed guard bypassed")
		}
	}
}
