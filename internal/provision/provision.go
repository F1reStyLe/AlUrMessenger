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
	"unicode"
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

// Seed назначает локального admin только при первом создании dev fixture.
// Существующие профили/роли не перезаписываются: повтор не отменяет ручное понижение.
func Seed(ctx context.Context, environment string, db *pgx.Conn) error {
	if environment != "development" {
		return errors.New("SEED_REQUIRES_DEVELOPMENT")
	}
	if err := Create(ctx, db, Project{DevProject, "Local development", DevIssuer, DevAudience}); err != nil {
		return err
	}
	for _, name := range []string{"admin", "alice", "bob"} {
		role := "user"
		if name == "admin" {
			role = "admin"
		}
		if _, err := db.Exec(ctx, `INSERT INTO chat.users(id,project_id,external_user_id,display_name,role) VALUES($1,$2,$3,$3,$4) ON CONFLICT(project_id,external_user_id) DO NOTHING`, uuid.NewString(), DevProject, name, role); err != nil {
			return err
		}
	}
	// Dev integration flags are enabled only before the first administrative edit.
	// Repeat seed must never reset the admin's versioned settings.
	_, err := db.Exec(ctx, `UPDATE chat.project_settings SET flags=flags || '{"allow_bots":true,"allow_webhooks":true}'::jsonb WHERE project_id=$1 AND settings_version=1 AND updated_by IS NULL`, DevProject)
	if err != nil {
		return err
	}
	return seedConversations(ctx, db)
}

// Token выпускается только в development; срок 15 минут, явный access purpose и RS256.
// Caller несёт ответственность за передачу результата пользователю без logging.
func Token(environment string, privatePEM []byte, subject string) (string, error) {
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
	// Development tokens prove identity only, exactly as tokens from the real Auth.
	claims := auth.Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: DevIssuer, Subject: subject, Audience: jwt.ClaimStrings{DevAudience}, IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)), ID: uuid.NewString()}, ProjectID: DevProject, TokenUse: "access"}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
}

// RoleAssignment identifies one existing Chat profile, never an Auth-global role.
// Operators use the external ID returned by /api/v1/me, scoped to the exact Project.
type RoleAssignment struct{ ProjectID, ExternalID, Role string }

// Validate rejects ambiguous scope and conversation roles before opening the DB.
func (r RoleAssignment) Validate() error {
	id, err := uuid.Parse(r.ProjectID)
	if err != nil || id == uuid.Nil || r.ProjectID != id.String() || strings.TrimSpace(r.ExternalID) != r.ExternalID || r.ExternalID == "" || len(r.ExternalID) > 256 || strings.IndexFunc(r.ExternalID, unicode.IsControl) != -1 || (r.Role != "user" && r.Role != "admin") {
		return errors.New("ROLE_ASSIGNMENT_INVALID")
	}
	return nil
}

// SetRole is an operator-only command; runtime credentials cannot modify role.
// Project → user lock order matches policy writes: once a demotion commits, a
// waiting policy mutation rechecks the DB role and cannot use a stale admin Actor.
// Missing users are not created here: identity must first be confirmed by Auth.
func SetRole(ctx context.Context, db *pgx.Conn, r RoleAssignment) error {
	if err := r.Validate(); err != nil {
		return err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var active bool
	if err := tx.QueryRow(ctx, "SELECT status='active' FROM chat.projects WHERE id=$1 FOR UPDATE", r.ProjectID).Scan(&active); err != nil {
		return err
	}
	if !active {
		return errors.New("PROJECT_INACTIVE")
	}
	result, err := tx.Exec(ctx, "UPDATE chat.users SET role=$3,updated_at=now() WHERE project_id=$1 AND external_user_id=$2", r.ProjectID, r.ExternalID, r.Role)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errors.New("USER_NOT_FOUND")
	}
	return tx.Commit(ctx)
}
