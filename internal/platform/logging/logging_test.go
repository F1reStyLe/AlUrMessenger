package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
)

// TestStructuredIdentityAndLevel: Проверяет JSON-поля идентичности и фильтрацию INFO при минимальном уровне WARN.
func TestStructuredIdentityAndLevel(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, config.Log{Level: slog.LevelWarn, Format: "json"}, config.Worker, "test")
	logger.Info("filtered")
	logger.Warn("delivery delayed", "event_id", "event-1")
	var entry map[string]any
	decoder := json.NewDecoder(&output)
	if err := decoder.Decode(&entry); err != nil {
		t.Fatal(err)
	}
	if entry["service"] != "chat-worker" || entry["environment"] != "test" || entry["event_id"] != "event-1" || entry["level"] != "WARN" {
		t.Fatalf("missing structured identity or level: %v", entry)
	}
	if decoder.More() {
		t.Fatal("info log must be filtered")
	}
}
