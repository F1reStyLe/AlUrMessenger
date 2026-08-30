// Package provision содержит operator-only provisioning и строго dev-only seed/token.
// Публичного HTTP endpoint создания Project или выдачи JWT здесь нет.
package provision

import (
	"context"
	"errors"
	"github.com/F1reStyLe/AlUrMessenger/internal/auth"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// Development identity constants не являются credentials; ключ создаётся dev-init.
const DevProject = "00000000-0000-4000-8000-000000000001"
const DevIssuer = "https://auth.local.dev"
const DevAudience = "alur-chat"

// Project используется только operator CLI с отдельными privileged credentials.
type Project struct{ ID, Name, Issuer, Audience string }

// Validate исключает пустые/неоднозначные trust bindings до открытия транзакции.
func (p Project) Validate() error {
	id, err := uuid.Parse(p.ID)
	u, e := url.Parse(p.Issuer)
	if err != nil || id == uuid.Nil || p.ID != id.String() || strings.TrimSpace(p.Name) == "" || utf8.RuneCountInString(p.Name) > 128 || e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(p.Issuer) > 2048 || strings.TrimSpace(p.Audience) == "" || len(p.Audience) > 256 {
		return errors.New("PROJECT_INVALID")
	}
	return nil
}

// Create повторяем только при точном совпадении bindings; не меняем существующий Project.
func Create(ctx context.Context, db *pgx.Conn, p Project) error {
	if err := p.Validate(); err != nil {
		return err
	}
	_, err := db.Exec(ctx, `INSERT INTO chat.projects(id,name,auth_issuer,auth_audience) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO NOTHING`, p.ID, p.Name, p.Issuer, p.Audience)
	if err != nil {
		return err
	}
	var matches bool
	err = db.QueryRow(ctx, "SELECT name=$2 AND auth_issuer=$3 AND auth_audience=$4 AND status='active' FROM chat.projects WHERE id=$1", p.ID, p.Name, p.Issuer, p.Audience).Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return errors.New("PROJECT_ALREADY_EXISTS_WITH_DIFFERENT_SETTINGS")
	}
	// Explicit default settings are created for every operator-provisioned Project.
	_, err = db.Exec(ctx, "INSERT INTO chat.project_settings(project_id) VALUES($1) ON CONFLICT(project_id) DO NOTHING", p.ID)
	return err
}

// Seed не назначает роли в БД: admin role приходит только из подписанного JWT.
// Existing profiles не перезаписываются; список ограничен одним явным dev Project.
func Seed(ctx context.Context, environment string, db *pgx.Conn) error {
	if environment != "development" {
		return errors.New("SEED_REQUIRES_DEVELOPMENT")
	}
	if err := Create(ctx, db, Project{DevProject, "Local development", DevIssuer, DevAudience}); err != nil {
		return err
	}
	for _, name := range []string{"admin", "alice", "bob"} {
		if _, err := db.Exec(ctx, `INSERT INTO chat.users(id,project_id,external_user_id,display_name) VALUES($1,$2,$3,$3) ON CONFLICT(project_id,external_user_id) DO NOTHING`, uuid.NewString(), DevProject, name); err != nil {
			return err
		}
	}
	// Dev integration flags are enabled only before the first administrative edit.
	// Repeat seed must never reset the admin's versioned settings.
	_, err := db.Exec(ctx, `UPDATE chat.project_settings SET flags=flags || '{"allow_bots":true,"allow_webhooks":true}'::jsonb WHERE project_id=$1 AND settings_version=1 AND updated_by IS NULL`, DevProject)
	return err
}

// Token выпускается только в development; срок 15 минут, явный access purpose и RS256.
// Caller несёт ответственность за передачу результата пользователю без logging.
func Token(environment string, privatePEM []byte, subject string, admin bool) (string, error) {
	if environment != "development" {
		return "", errors.New("DEV_TOKEN_REQUIRES_DEVELOPMENT")
	}
	if strings.TrimSpace(subject) == "" || len(subject) > 256 {
		return "", errors.New("SUBJECT_INVALID")
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM(privatePEM)
	if err != nil || key.N.BitLen() < 2048 {
		return "", errors.New("DEV_KEY_INVALID")
	}
	now := time.Now()
	roles := []string{"user"}
	if admin {
		roles = []string{"admin"}
	}
	claims := auth.Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: DevIssuer, Subject: subject, Audience: jwt.ClaimStrings{DevAudience}, IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)), ID: uuid.NewString()}, ProjectID: DevProject, Roles: roles, TokenUse: "access"}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
}
