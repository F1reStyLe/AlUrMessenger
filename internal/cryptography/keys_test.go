package cryptography

import (
	"bytes"
	"reflect"
	"testing"
)

// TestEnvelopeIsolation verifies randomized encryption and authentication failures
// for every context dimension; a valid ciphertext cannot be copied between records.
func TestEnvelopeIsolation(t *testing.T) {
	cfg, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e, err := keys.Seal("p", "c", "m", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	other, _ := keys.Seal("p", "c", "m", []byte("secret"))
	if bytes.Equal(e.Ciphertext, other.Ciphertext) {
		t.Fatal("deterministic ciphertext")
	}
	plain, err := keys.Open("p", "c", "m", e)
	if err != nil || string(plain) != "secret" {
		t.Fatal("roundtrip failed")
	}
	for _, ids := range [][3]string{{"q", "c", "m"}, {"p", "d", "m"}, {"p", "c", "n"}} {
		if _, err = keys.Open(ids[0], ids[1], ids[2], e); err == nil {
			t.Fatal("context substitution accepted")
		}
	}
	e.Ciphertext[0] ^= 1
	if _, err = keys.Open("p", "c", "m", e); err == nil {
		t.Fatal("tampering accepted")
	}
}

// TestReportEnvelopeIsolation proves that moderation descriptions cannot be
// substituted between reports or opened through the message-key namespace.
func TestReportEnvelopeIsolation(t *testing.T) {
	cfg, _ := Generate()
	keys, _ := New(cfg)
	envelope, err := keys.SealReport("p", "r", []byte("private complaint"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := keys.OpenReport("p", "r", envelope)
	if err != nil || string(plain) != "private complaint" {
		t.Fatal("report roundtrip failed", err)
	}
	if _, err = keys.OpenReport("p", "other", envelope); err == nil {
		t.Fatal("cross-report substitution accepted")
	}
	if _, err = keys.OpenReport("other", "r", envelope); err == nil {
		t.Fatal("cross-Project substitution accepted")
	}
	if _, err = keys.Open("p", "conversation", "r", envelope); err == nil {
		t.Fatal("report ciphertext opened as a message")
	}
}

// TestRotation keeps historical reads/search/retry receipts valid while all new
// writes switch to distinct active versions; removing an old key fails closed.
func TestRotation(t *testing.T) {
	cfg, _ := Generate()
	old, _ := New(cfg)
	envelope, _ := old.Seal("p", "c", "m", []byte("retained"))
	fingerprint, _ := old.Fingerprint("p", "v1", []byte("command"))
	_, tokens, _ := old.Index("p", "retained")
	next, _ := Generate()
	cfg.EncryptionKeys["v2"] = next.EncryptionKeys["v1"]
	cfg.SearchKeys["v2"] = next.SearchKeys["v1"]
	cfg.FingerprintKeys["v2"] = next.FingerprintKeys["v1"]
	cfg.ActiveEncryption = "v2"
	cfg.ActiveSearch = "v2"
	cfg.ActiveFingerprint = "v2"
	rotated, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := rotated.Open("p", "c", "m", envelope)
	if err != nil || string(plain) != "retained" {
		t.Fatal("old ciphertext unreadable", err)
	}
	candidate, err := rotated.Fingerprint("p", "v1", []byte("command"))
	if err != nil || !bytes.Equal(candidate, fingerprint) {
		t.Fatal("old idempotency fingerprint changed")
	}
	queries, err := rotated.Queries("p", "retained")
	if err != nil || !reflect.DeepEqual(queries["v1"], tokens) || len(queries) != 2 {
		t.Fatal("old search version lost")
	}
	sealed, err := rotated.Seal("p", "c", "n", plain)
	if err != nil || sealed.KeyVersion != "v2" {
		t.Fatal("active encryption not changed")
	}
	delete(cfg.EncryptionKeys, "v1")
	retired, _ := New(cfg)
	if _, err = retired.Open("p", "c", "m", envelope); err == nil {
		t.Fatal("unknown key version accepted")
	}
}

// TestBlindSearch checks Unicode normalization, whole-word token matching and tenant separation.
func TestBlindSearch(t *testing.T) {
	cfg, _ := Generate()
	k, _ := New(cfg)
	v, a, err := k.Index("p", "CAFÉ Привет café")
	if err != nil || len(a) != 2 {
		t.Fatal(err)
	}
	q, err := k.Queries("p", "cafe\u0301 ПРИВЕТ")
	if err != nil || len(q[v]) != 2 || q[v][0] != a[0] || q[v][1] != a[1] {
		t.Fatal("normalization mismatch")
	}
	_, b, _ := k.Index("other", "CAFÉ Привет")
	if a[0] == b[0] {
		t.Fatal("cross-Project token leak")
	}
	if _, err = k.Queries("p", "one two three four five six seven eight nine"); err == nil {
		t.Fatal("unbounded query")
	}
}
