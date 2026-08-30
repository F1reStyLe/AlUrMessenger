// Package repository реализует Project-scoped identity storage на PostgreSQL.
package repository

import (
	"context"
	"errors"

	"github.com/F1reStyLe/AlUrMessenger/internal/auth"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store держит runtime pool; отсутствие Project ID в SQL не допускается интерфейсом.
type Store struct{ DB *pgxpool.Pool }

// AuthProject возвращает только trusted binding; не создаёт Project из claims.
func (s *Store) AuthProject(ctx context.Context, id string) (auth.Project, error) {
	p := auth.Project{ID: id}
	err := s.DB.QueryRow(ctx, "SELECT auth_issuer,auth_audience,status='active' FROM chat.projects WHERE id=$1", id).Scan(&p.Issuer, &p.Audience, &p.Active)
	return p, err
}

// scanUser централизует DTO projection; ban metadata/external ID не сериализуются публично.
func scanUser(row pgx.Row) (identity.User, error) {
	var u identity.User
	err := row.Scan(&u.ID, &u.DisplayName, &u.AvatarURL, &u.Kind, &u.Status, &u.LastSeenAt, &u.ExternalID, &u.Banned, &u.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		err = identity.ErrNotFound
	}
	return u, err
}

// columns остаётся одинаковым для всех profile projections.
const columns = "id::text,display_name,avatar_url,kind,status,last_seen_at,COALESCE(external_user_id,''),banned_at IS NOT NULL,role"

// EnsureUser сериализует policy с shared Project lock, а uniqueness разрешает
// конкурентный первый вход. No-op upsert возвращает существующий профиль, не меняя имя/роль.
func (s *Store) EnsureUser(ctx context.Context, id auth.Identity) (identity.User, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return identity.User{}, err
	}
	defer tx.Rollback(ctx)
	var active bool
	if err = tx.QueryRow(ctx, "SELECT status='active' FROM chat.projects WHERE id=$1 FOR SHARE", id.ProjectID).Scan(&active); err != nil || !active {
		return identity.User{}, auth.ErrUnauthenticated
	}
	u, err := scanUser(tx.QueryRow(ctx, `INSERT INTO chat.users(id,project_id,external_user_id,display_name) VALUES($1,$2,$3,$4)
	 ON CONFLICT(project_id,external_user_id) DO UPDATE SET external_user_id=EXCLUDED.external_user_id RETURNING `+columns, uuid.NewString(), id.ProjectID, id.ExternalID, uuid.NewString()))
	if err != nil {
		return u, err
	}
	return u, tx.Commit(ctx)
}

// GetUser скрывает чужую строку как отсутствие ресурса за счёт SQL tenant predicate.
func (s *Store) GetUser(ctx context.Context, projectID, userID string) (identity.User, error) {
	return scanUser(s.DB.QueryRow(ctx, "SELECT "+columns+" FROM chat.users WHERE project_id=$1 AND id=$2", projectID, userID))
}

// ListUsers использует стабильный UUID cursor внутри Project, без OFFSET и чужих IDs.
func (s *Store) ListUsers(ctx context.Context, projectID, after string, limit int) ([]identity.User, error) {
	if after == "" {
		after = uuid.Nil.String()
	}
	rows, err := s.DB.Query(ctx, "SELECT "+columns+" FROM chat.users WHERE project_id=$1 AND id>$2 ORDER BY id LIMIT $3", projectID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []identity.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, u)
	}
	return result, rows.Err()
}

// PatchUser проверяет ban в той же транзакции и под lock, что и обновление профиля.
// Banned actor может читать профиль, но не писать; admin не обходит этот запрет.
func (s *Store) PatchUser(ctx context.Context, actor identity.Actor, patch identity.ProfilePatch) (identity.User, error) {
	if err := patch.Validate(); err != nil {
		return identity.User{}, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return identity.User{}, err
	}
	defer tx.Rollback(ctx)
	var active bool
	if err = tx.QueryRow(ctx, "SELECT status='active' FROM chat.projects WHERE id=$1 FOR SHARE", actor.ProjectID).Scan(&active); err != nil || !active {
		return identity.User{}, auth.ErrUnauthenticated
	}
	u, err := scanUser(tx.QueryRow(ctx, "SELECT "+columns+" FROM chat.users WHERE project_id=$1 AND id=$2 FOR UPDATE", actor.ProjectID, actor.User.ID))
	if err != nil {
		return u, err
	}
	if u.Banned {
		return u, identity.ErrBanned
	}
	u, err = scanUser(tx.QueryRow(ctx, "UPDATE chat.users SET display_name=COALESCE($3,display_name),avatar_url=COALESCE($4,avatar_url),updated_at=now() WHERE project_id=$1 AND id=$2 RETURNING "+columns, actor.ProjectID, actor.User.ID, patch.DisplayName, patch.AvatarURL))
	if err != nil {
		return u, err
	}
	return u, tx.Commit(ctx)
}
