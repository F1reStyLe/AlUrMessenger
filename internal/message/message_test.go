package message

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Existing send receipts must still validate after adding optional reply support.
func TestLegacyFingerprintAndReplyIdentity(t *testing.T) {
	p := Send{ClientID: "01900000-0000-4000-8000-000000000010", Type: "TEXT", Content: Content{Text: "hello"}}
	before, _ := p.Canonical("conversation")
	const legacy = `{"conversation_id":"conversation","client_message_id":"01900000-0000-4000-8000-000000000010","type":"TEXT","content":{"text":"hello"}}`
	if string(before) != legacy {
		t.Fatal("legacy dedup fingerprint changed")
	}
	id := uuid.NewString()
	p.ReplyTo = &id
	after, _ := p.Canonical("conversation")
	if bytes.Equal(before, after) {
		t.Fatal("reply target missing from fingerprint")
	}
	if err := p.Validate(false); err != nil {
		t.Fatal(err)
	}
	bad := "not-a-uuid"
	p.ReplyTo = &bad
	if p.Validate(false) == nil {
		t.Fatal("invalid reply accepted")
	}
}

// JSON must omit the body entirely for tombstones; an empty text object can be
// mistaken for an edit and metadata/reply previews could disclose removed content.
func TestTombstoneJSON(t *testing.T) {
	m := Message{ID: uuid.NewString(), Status: "deleted", ExpiresAt: time.Now()}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	json.Unmarshal(data, &fields)
	for _, key := range []string{"content", "metadata", "reply"} {
		if _, exists := fields[key]; exists {
			t.Fatal("tombstone field", key)
		}
	}
}
