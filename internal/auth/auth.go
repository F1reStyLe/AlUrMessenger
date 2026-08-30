// Package auth проверяет внешние access JWT; production tokens сервис не выпускает.
package auth

import (
	"context"
	"crypto/rsa"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// ErrUnauthenticated скрывает причины отказа, содержимое JWT и существование Project.
var ErrUnauthenticated = errors.New("UNAUTHENTICATED")

// Project задаёт trusted issuer/audience binding, полученный из operator-provisioned БД.
type Project struct {
	ID, Issuer, Audience string
	Active               bool
}

// Identity содержит проверенную внешнюю идентичность. Admin заполняется application
// service только из Chat DB после provisioning; внешние роли не являются правами Chat.
type Identity struct {
	ProjectID, ExternalID string
	Admin                 bool
}

// Claims — контракт offline dev-rsa fixture. Legacy Roles декодируются, но никогда
// не дают прав Chat; token_use не позволяет принять dev refresh token как access.
type Claims struct {
	jwt.RegisteredClaims
	ProjectID string   `json:"project_id"`
	Roles     []string `json:"roles,omitempty"`
	TokenUse  string   `json:"token_use"`
}

// Resolver выполняет только trusted config lookup; непроверенный project_id ещё не actor.
type Resolver interface {
	AuthProject(context.Context, string) (Project, error)
}

// Verifier отделяет криптографию от HTTP/application и позволяет позже подключить JWKS.
type Verifier interface {
	Verify(context.Context, string) (Identity, error)
}

// RSA — только offline development fixture: не поддерживает отзыв внешней сессии.
// Доверяет локальному public key и project bindings, не JWT jku/x5u/kid URLs.
type RSA struct {
	key      *rsa.PublicKey
	projects Resolver
}

// Load читает только public PEM при startup. Небольшой лимит предотвращает чтение
// случайного большого файла; ключ короче 2048 бит не принимается.
func Load(path string, projects Resolver) (*RSA, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("AUTH_PUBLIC_KEY_INVALID")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(data) > 16384 {
		return nil, errors.New("AUTH_PUBLIC_KEY_INVALID")
	}
	return New(data, projects)
}

// New конструирует verifier без сетевых обращений; rotation требует нового instance/restart.
func New(pem []byte, projects Resolver) (*RSA, error) {
	key, err := jwt.ParseRSAPublicKeyFromPEM(pem)
	if err != nil || key.N.BitLen() < 2048 {
		return nil, errors.New("AUTH_PUBLIC_KEY_INVALID")
	}
	return &RSA{key: key, projects: projects}, nil
}

// WithProjects returns an immutable verifier copy after the public key has been
// validated before bind. No mutable trust configuration is shared with requests.
func (v *RSA) WithProjects(projects Resolver) *RSA { return &RSA{key: v.key, projects: projects} }

// Verify сначала извлекает только Project ID для trusted lookup, затем повторно
// разбирает JWT с проверкой подписи, RS256, iss/aud/exp/nbf/iat и token purpose.
// Ошибки разбора не логируются и никогда не превращаются в anonymous actor.
func (v *RSA) Verify(ctx context.Context, raw string) (Identity, error) {
	if v.projects == nil || len(raw) == 0 || len(raw) > 8192 {
		return Identity{}, ErrUnauthenticated
	}
	var hint Claims
	if _, _, err := jwt.NewParser().ParseUnverified(raw, &hint); err != nil {
		return Identity{}, ErrUnauthenticated
	}
	projectID, err := uuid.Parse(hint.ProjectID)
	if err != nil || projectID == uuid.Nil {
		return Identity{}, ErrUnauthenticated
	}
	project, err := v.projects.AuthProject(ctx, projectID.String())
	if err != nil || !project.Active {
		return Identity{}, ErrUnauthenticated
	}
	var claims Claims
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(project.Issuer), jwt.WithAudience(project.Audience),
		jwt.WithExpirationRequired(), jwt.WithLeeway(30*time.Second), jwt.WithIssuedAt(), jwt.WithStrictDecoding())
	token, err := parser.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) { return v.key, nil })
	if err != nil || !token.Valid || claims.ProjectID != project.ID || claims.TokenUse != "access" || strings.TrimSpace(claims.Subject) == "" || len(claims.Subject) > 256 {
		return Identity{}, ErrUnauthenticated
	}
	// Legacy development JWT roles are intentionally ignored, including "admin".
	return Identity{ProjectID: project.ID, ExternalID: claims.Subject}, nil
}
