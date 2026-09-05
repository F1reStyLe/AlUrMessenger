// Package cryptography owns versioned AES-GCM payload encryption and blind search
// indexes. Keys are independent by purpose and never appear in database records.
package cryptography

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// ErrCrypto is safe to return as an unavailable operation; it contains no key/plaintext.
var ErrCrypto = errors.New("CONTENT_CRYPTO_UNAVAILABLE")
var ErrTokens = errors.New("TEXT_TOKEN_LIMIT")

// Config is a secret-file format. Retain old purpose-specific versions until data
// re-encryption/reindex and the idempotency retry window no longer require them.
type Config struct {
	ActiveEncryption  string            `json:"active_encryption"`
	ActiveSearch      string            `json:"active_search"`
	ActiveFingerprint string            `json:"active_fingerprint"`
	EncryptionKeys    map[string]string `json:"encryption_keys"`
	SearchKeys        map[string]string `json:"search_keys"`
	FingerprintKeys   map[string]string `json:"fingerprint_keys"`
}

// Keys is immutable after construction, so HTTP handlers can safely share it.
type Keys struct {
	activeEncryption, activeSearch, activeFingerprint string
	encryption, search, fingerprint                   map[string][]byte
}

// Envelope binds ciphertext to its message context; nonce is public and unique per seal.
type Envelope struct {
	Ciphertext, Nonce []byte
	KeyVersion        string
	PayloadVersion    int
}

// Load bounds the secret file and redacts all parsing/file errors before startup logs.
func Load(path string) (*Keys, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrCrypto
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(data) > 16384 {
		return nil, ErrCrypto
	}
	var cfg Config
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if dec.Decode(&cfg) != nil {
		return nil, ErrCrypto
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, ErrCrypto
	}
	return New(cfg)
}

// New validates independent 256-bit keys and a bounded version vocabulary.
func New(cfg Config) (*Keys, error) {
	decode := func(keys map[string]string, active string) (map[string][]byte, error) {
		if len(keys) == 0 || len(keys) > 8 || keys[active] == "" {
			return nil, ErrCrypto
		}
		out := map[string][]byte{}
		valid := regexp.MustCompile(`^[a-zA-Z0-9_-]{1,32}$`)
		for version, value := range keys {
			key, err := hex.DecodeString(value)
			if err != nil || len(key) != 32 || !valid.MatchString(version) {
				return nil, ErrCrypto
			}
			out[version] = key
		}
		return out, nil
	}
	e, err := decode(cfg.EncryptionKeys, cfg.ActiveEncryption)
	if err != nil {
		return nil, err
	}
	s, err := decode(cfg.SearchKeys, cfg.ActiveSearch)
	if err != nil {
		return nil, err
	}
	f, err := decode(cfg.FingerprintKeys, cfg.ActiveFingerprint)
	if err != nil {
		return nil, err
	}
	return &Keys{cfg.ActiveEncryption, cfg.ActiveSearch, cfg.ActiveFingerprint, e, s, f}, nil
}

// Generate creates independent development secrets. It does not write or log them.
func Generate() (Config, error) {
	randomKey := func() (string, error) {
		b := make([]byte, 32)
		_, err := rand.Read(b)
		return hex.EncodeToString(b), err
	}
	e, err := randomKey()
	if err != nil {
		return Config{}, ErrCrypto
	}
	s, err := randomKey()
	if err != nil {
		return Config{}, ErrCrypto
	}
	f, err := randomKey()
	if err != nil {
		return Config{}, ErrCrypto
	}
	return Config{"v1", "v1", "v1", map[string]string{"v1": e}, map[string]string{"v1": s}, map[string]string{"v1": f}}, nil
}

// derived separates equal words/fingerprints across Projects and cryptographic purposes.
func derived(master []byte, project, purpose, version string) ([]byte, error) {
	if len(master) != 32 {
		return nil, ErrCrypto
	}
	return hkdf.Key(sha256.New, master, nil, "alur/"+purpose+"/"+version+"/"+project, 32)
}

// aead uses AES-256-GCM; a missing retained key fails closed rather than reading garbage.
func (k *Keys) aead(project, version string) (cipher.AEAD, error) {
	return k.aeadPurpose(project, "message", version)
}

func (k *Keys) aeadPurpose(project, purpose, version string) (cipher.AEAD, error) {
	key, err := derived(k.encryption[version], project, purpose, version)
	if err != nil {
		return nil, ErrCrypto
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrCrypto
	}
	return cipher.NewGCM(block)
}

func purposeAAD(purpose, project, resource, version string, payloadVersion int) []byte {
	b, _ := json.Marshal([]any{purpose, project, resource, version, payloadVersion})
	return b
}

// SealReport uses a separate derived key and AAD namespace from message bodies.
func (k *Keys) SealReport(project, report string, plain []byte) (Envelope, error) {
	version := k.activeEncryption
	a, err := k.aeadPurpose(project, "report", version)
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return Envelope{}, ErrCrypto
	}
	return Envelope{a.Seal(nil, nonce, plain, purposeAAD("report", project, report, version, 1)), nonce, version, 1}, nil
}

func (k *Keys) OpenReport(project, report string, envelope Envelope) ([]byte, error) {
	a, err := k.aeadPurpose(project, "report", envelope.KeyVersion)
	if err != nil || envelope.PayloadVersion != 1 || len(envelope.Nonce) != a.NonceSize() {
		return nil, ErrCrypto
	}
	plain, err := a.Open(nil, envelope.Nonce, envelope.Ciphertext, purposeAAD("report", project, report, envelope.KeyVersion, envelope.PayloadVersion))
	if err != nil {
		return nil, ErrCrypto
	}
	return plain, nil
}

// aad is unambiguous structured context, including purpose and both format/key versions.
func aad(project, conversation, message, version string, payloadVersion int) []byte {
	b, _ := json.Marshal([]any{"message", project, conversation, message, version, payloadVersion})
	return b
}

// Seal always generates a fresh nonce, including when encrypting equal plaintext.
func (k *Keys) Seal(project, conversation, message string, plain []byte) (Envelope, error) {
	version := k.activeEncryption
	a, err := k.aead(project, version)
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return Envelope{}, ErrCrypto
	}
	return Envelope{a.Seal(nil, nonce, plain, aad(project, conversation, message, version, 1)), nonce, version, 1}, nil
}

// Open must only be called after membership authorization. AAD prevents moving a
// valid encrypted payload to another Project, conversation, message or format version.
func (k *Keys) Open(project, conversation, message string, e Envelope) ([]byte, error) {
	a, err := k.aead(project, e.KeyVersion)
	if err != nil || e.PayloadVersion != 1 || len(e.Nonce) != a.NonceSize() {
		return nil, ErrCrypto
	}
	plain, err := a.Open(nil, e.Nonce, e.Ciphertext, aad(project, conversation, message, e.KeyVersion, e.PayloadVersion))
	if err != nil {
		return nil, ErrCrypto
	}
	return plain, nil
}

// words implements NFC + Unicode case folding and whole letter/digit tokens only.
// Excess tokens reject the request; the tail of a message is never silently unindexed.
func words(text string, max int) ([]string, error) {
	normalized := norm.NFC.String(cases.Fold().String(norm.NFC.String(text)))
	seen := map[string]bool{}
	for _, word := range strings.FieldsFunc(normalized, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		seen[word] = true
		if len(seen) > max {
			return nil, ErrTokens
		}
	}
	result := make([]string, 0, len(seen))
	for word := range seen {
		result = append(result, word)
	}
	sort.Strings(result)
	return result, nil
}

// blind hashes normalized words with an independent Project search key.
func (k *Keys) blind(project, version string, tokens []string) ([]string, error) {
	key, err := derived(k.search[version], project, "search", version)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(tokens))
	for _, word := range tokens {
		m := hmac.New(sha256.New, key)
		m.Write([]byte(word))
		out = append(out, hex.EncodeToString(m.Sum(nil)))
	}
	return out, nil
}

// Index stores the active search version alongside the blind tokens.
func (k *Keys) Index(project, text string) (string, []string, error) {
	w, err := words(text, 2048)
	if err != nil {
		return "", nil, err
	}
	tokens, err := k.blind(project, k.activeSearch, w)
	return k.activeSearch, tokens, err
}

// Queries searches every retained version, permitting independent key rotation.
func (k *Keys) Queries(project, text string) (map[string][]string, error) {
	w, err := words(text, 8)
	if err != nil || len(w) == 0 {
		return nil, ErrTokens
	}
	out := map[string][]string{}
	for version := range k.search {
		tokens, err := k.blind(project, version, w)
		if err != nil {
			return nil, err
		}
		out[version] = tokens
	}
	return out, nil
}

// FingerprintVersion is persisted with the idempotency row so retries use its old key.
func (k *Keys) FingerprintVersion() string { return k.activeFingerprint }

// Fingerprint binds a canonical command without exposing short plaintext to guessing.
func (k *Keys) Fingerprint(project, version string, command []byte) ([]byte, error) {
	key, err := derived(k.fingerprint[version], project, "idempotency", version)
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, key)
	m.Write(command)
	return m.Sum(nil), nil
}
