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

// A forward is a command, not user-supplied copied content. Its compact
// canonical form binds idempotency to both the target conversation and source.
func TestForwardValidationAndFingerprint(t *testing.T) {
	source := uuid.NewString()
	p := Send{ClientID: uuid.NewString(), ForwardFrom: &source}
	if err := p.Validate(false); err != nil {
		t.Fatal(err)
	}
	got, err := p.Canonical("target")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"conversation_id":"target","client_message_id":"` + p.ClientID + `","forwarded_from_message_id":"` + source + `"}`
	if string(got) != want {
		t.Fatalf("unexpected canonical command: %s", got)
	}
	if err := p.Validate(true); err == nil {
		t.Fatal("internal SYSTEM forward accepted")
	}

	for _, body := range []string{
		`{"client_message_id":"` + p.ClientID + `","forwarded_from_message_id":"` + source + `","type":""}`,
		`{"client_message_id":"` + p.ClientID + `","forwarded_from_message_id":"` + source + `","content":{}}`,
		`{"client_message_id":"` + p.ClientID + `","forwarded_from_message_id":"` + source + `","metadata":{}}`,
		`{"client_message_id":"` + p.ClientID + `","forwarded_from_message_id":"` + source + `","reply_to_message_id":null}`,
	} {
		var decoded Send
		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			t.Fatal(err)
		}
		if err := decoded.Validate(false); err == nil {
			t.Fatal("mixed forward command accepted:", body)
		}
	}
	var decoded Send
	if err := json.Unmarshal([]byte(`{"client_message_id":"`+p.ClientID+`","forwarded_from_message_id":"`+source+`","unknown":true}`), &decoded); err == nil {
		t.Fatal("unknown command field accepted")
	}
}

func TestImageCommandRequiresExactlyOneAttachment(t *testing.T) {
	p := Send{ClientID: uuid.NewString(), Type: "IMAGE", Content: Content{Caption: "caption"}, AttachmentIDs: []string{uuid.NewString()}}
	if err := p.Validate(false); err != nil {
		t.Fatal(err)
	}
	canonical, _ := p.Canonical("conversation")
	if !bytes.Contains(canonical, []byte(p.AttachmentIDs[0])) {
		t.Fatal("attachment identity missing from dedup fingerprint")
	}
	for _, invalid := range []Send{
		{ClientID: p.ClientID, Type: "IMAGE", Content: Content{Text: "text"}, AttachmentIDs: p.AttachmentIDs},
		{ClientID: p.ClientID, Type: "IMAGE", Content: Content{Caption: "caption"}},
		{ClientID: p.ClientID, Type: "IMAGE", AttachmentIDs: []string{"invalid"}},
		{ClientID: p.ClientID, Type: "TEXT", Content: Content{Text: "text"}, AttachmentIDs: p.AttachmentIDs},
	} {
		if invalid.Validate(false) == nil {
			t.Fatal("invalid IMAGE shape accepted", invalid)
		}
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
	for _, key := range []string{"content", "metadata", "reply", "forward"} {
		if _, exists := fields[key]; exists {
			t.Fatal("tombstone field", key)
		}
	}
}
