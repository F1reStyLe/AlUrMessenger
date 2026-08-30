package devinit

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInitializationIsDevelopmentOnlyAndRepeatable защищает от случайной ротации
// credentials при повторном запуске и от создания secrets в production.
func TestInitializationIsDevelopmentOnlyAndRepeatable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	if Initialize("production", dir) == nil {
		t.Fatal("production accepted")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("production wrote files")
	}
	if err := Initialize("development", dir); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "postgres-url"))
	if err := Initialize("development", dir); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "postgres-url"))
	if string(before) != string(after) {
		t.Fatal("secret rotated")
	}
	// Windows cleanup требует снять read-only bit у generated files.
	t.Cleanup(func() {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			_ = os.Chmod(filepath.Join(dir, e.Name()), 0600)
		}
	})
}
