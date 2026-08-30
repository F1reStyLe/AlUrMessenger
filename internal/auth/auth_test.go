package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"testing"
	"time"
)

// projectFixture supplies trusted bindings independently of token claims.
type projectFixture struct{ project Project }

func (f projectFixture) AuthProject(_ context.Context, id string) (Project, error) {
	if id != f.project.ID {
		return Project{}, ErrUnauthenticated
	}
	return f.project, nil
}

// TestAccessContract covers algorithm confusion, signatures and every trust claim.
func TestAccessContract(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	p := Project{uuid.NewString(), "https://issuer.example", "chat", true}
	v, err := New(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: raw}), projectFixture{p})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"valid", "admin", "signature", "algorithm", "issuer", "audience", "expired", "no-exp", "future-nbf", "future-iat", "refresh", "moderator", "no-roles", "project", "empty-sub", "disabled"} {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			c := Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: p.Issuer, Subject: "external-user", Audience: jwt.ClaimStrings{p.Audience}, ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)), IssuedAt: jwt.NewNumericDate(now)}, ProjectID: p.ID, Roles: []string{"user"}, TokenUse: "access"}
			var signingKey any = key
			method := jwt.SigningMethod(jwt.SigningMethodRS256)
			verifier := v
			switch name {
			case "admin":
				c.Roles = []string{"admin"}
			case "signature":
				signingKey = other
			case "algorithm":
				method = jwt.SigningMethodHS256
				signingKey = []byte("not-an-rsa-key")
			case "issuer":
				c.Issuer = "https://wrong"
			case "audience":
				c.Audience = jwt.ClaimStrings{"wrong"}
			case "expired":
				c.ExpiresAt = jwt.NewNumericDate(now.Add(-time.Minute))
			case "no-exp":
				c.ExpiresAt = nil
			case "future-nbf":
				c.NotBefore = jwt.NewNumericDate(now.Add(time.Minute))
			case "future-iat":
				c.IssuedAt = jwt.NewNumericDate(now.Add(time.Minute))
			case "refresh":
				c.TokenUse = "refresh"
			case "moderator":
				c.Roles = []string{"moderator"}
			case "no-roles":
				c.Roles = nil
			case "project":
				c.ProjectID = uuid.NewString()
			case "empty-sub":
				c.Subject = " "
			case "disabled":
				disabled := p
				disabled.Active = false
				verifier = &RSA{key: &key.PublicKey, projects: projectFixture{disabled}}
			}
			token, err := jwt.NewWithClaims(method, c).SignedString(signingKey)
			if err != nil {
				t.Fatal(err)
			}
			id, err := verifier.Verify(t.Context(), token)
			if name == "valid" || name == "admin" || name == "moderator" || name == "no-roles" {
				if err != nil || id.ProjectID != p.ID || id.Admin {
					t.Fatal("valid access rejected")
				}
			} else if err != ErrUnauthenticated {
				t.Fatal("invalid access accepted")
			}
		})
	}
}
