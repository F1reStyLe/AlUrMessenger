// Package repository persists blacklist configuration and performs write-time matching.
package repository

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/F1reStyLe/AlUrMessenger/internal/auth"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/moderation"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ DB *pgxpool.Pool }

const projection = `SELECT id::text,normalized_word,enabled,resource_version,created_at,updated_at FROM chat.blacklist_entries WHERE project_id=$1`

func scan(row pgx.Row) (moderation.Entry, error) {
	var result moderation.Entry
	err := row.Scan(&result.ID, &result.Word, &result.Enabled, &result.Version, &result.CreatedAt, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, identity.ErrNotFound
	}
	return result, err
}

// adminTx repeats mutable role/ban checks under the stable Project-first lock.
func adminTx(ctx context.Context, tx pgx.Tx, actor identity.Actor) error {
	var active, admin, banned bool
	if err := tx.QueryRow(ctx, `SELECT p.status='active',u.role='admin',u.banned_at IS NOT NULL FROM chat.projects p
 JOIN chat.users u ON u.project_id=p.id WHERE p.id=$1 AND u.id=$2 FOR UPDATE OF p FOR SHARE OF u`, actor.ProjectID, actor.User.ID).Scan(&active, &admin, &banned); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.ErrUnauthenticated
		}
		return err
	}
	if !active {
		return auth.ErrUnauthenticated
	}
	if banned {
		return identity.ErrBanned
	}
	if !admin {
		return policy.ErrForbidden
	}
	return nil
}

func audit(ctx context.Context, tx pgx.Tx, actor identity.Actor, action, id string, trace policy.Trace, fields map[string]any) error {
	auditID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO chat.audit_logs(id,project_id,actor_id,actor_type,action,resource_type,resource_id,metadata,ip,request_id)
 VALUES($1,$2,$3,'user',$4,'blacklist_entry',$5,$6,NULLIF($7,'')::inet,$8)`, auditID.String(), actor.ProjectID, actor.User.ID, action, id, metadata, trace.IP, trace.RequestID)
	return err
}

func (s *Store) List(ctx context.Context, actor identity.Actor, after string, limit int) ([]moderation.Entry, error) {
	var active, admin, banned bool
	if err := s.DB.QueryRow(ctx, `SELECT p.status='active',u.role='admin',u.banned_at IS NOT NULL FROM chat.projects p
 JOIN chat.users u ON u.project_id=p.id WHERE p.id=$1 AND u.id=$2`, actor.ProjectID, actor.User.ID).Scan(&active, &admin, &banned); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, auth.ErrUnauthenticated
		}
		return nil, err
	}
	if !active {
		return nil, auth.ErrUnauthenticated
	}
	if banned {
		return nil, identity.ErrBanned
	}
	if !admin {
		return nil, policy.ErrForbidden
	}
	if after == "" {
		after = uuid.Nil.String()
	}
	rows, err := s.DB.Query(ctx, projection+" AND id>$2 ORDER BY id LIMIT $3", actor.ProjectID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []moderation.Entry{}
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) Create(ctx context.Context, actor identity.Actor, word string, trace policy.Trace) (moderation.Entry, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return moderation.Entry{}, err
	}
	defer tx.Rollback(ctx)
	if err = adminTx(ctx, tx, actor); err != nil {
		return moderation.Entry{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return moderation.Entry{}, err
	}
	result, err := scan(tx.QueryRow(ctx, `INSERT INTO chat.blacklist_entries(id,project_id,normalized_word,created_by)
 VALUES($1,$2,$3,$4) RETURNING id::text,normalized_word,enabled,resource_version,created_at,updated_at`, id.String(), actor.ProjectID, word, actor.User.ID))
	var violation *pgconn.PgError
	if errors.As(err, &violation) && violation.Code == "23505" {
		return moderation.Entry{}, policy.ErrConflict
	}
	if err != nil {
		return moderation.Entry{}, err
	}
	if err = audit(ctx, tx, actor, "blacklist.created", result.ID, trace, map[string]any{"enabled": true}); err != nil {
		return moderation.Entry{}, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) Update(ctx context.Context, actor identity.Actor, id string, patch moderation.Patch, trace policy.Trace) (moderation.Entry, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return moderation.Entry{}, err
	}
	defer tx.Rollback(ctx)
	if err = adminTx(ctx, tx, actor); err != nil {
		return moderation.Entry{}, err
	}
	result, err := scan(tx.QueryRow(ctx, `UPDATE chat.blacklist_entries SET enabled=$3,resource_version=resource_version+1,updated_at=clock_timestamp()
 WHERE project_id=$1 AND id=$2 AND resource_version=$4 RETURNING id::text,normalized_word,enabled,resource_version,created_at,updated_at`, actor.ProjectID, id, *patch.Enabled, patch.ExpectedVersion))
	if errors.Is(err, identity.ErrNotFound) {
		var exists bool
		if e := tx.QueryRow(ctx, "SELECT true FROM chat.blacklist_entries WHERE project_id=$1 AND id=$2", actor.ProjectID, id).Scan(&exists); e == nil {
			return moderation.Entry{}, policy.ErrConflict
		}
	}
	if err != nil {
		return moderation.Entry{}, err
	}
	if err = audit(ctx, tx, actor, "blacklist.updated", id, trace, map[string]any{"changed_fields": []string{"enabled"}}); err != nil {
		return moderation.Entry{}, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) Delete(ctx context.Context, actor identity.Actor, id string, trace policy.Trace) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = adminTx(ctx, tx, actor); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, "DELETE FROM chat.blacklist_entries WHERE project_id=$1 AND id=$2", actor.ProjectID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return identity.ErrNotFound
	}
	if err = audit(ctx, tx, actor, "blacklist.deleted", id, trace, map[string]any{}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CheckContent is invoked inside every content-writing transaction after its
// Project lock. It returns only a stable rejection code, never the matched word.
func CheckContent(ctx context.Context, tx pgx.Tx, project, text string) error {
	words := moderation.Words(text)
	if len(words) == 0 {
		return nil
	}
	var enabled bool
	var mode string
	if err := tx.QueryRow(ctx, "SELECT blacklist_enabled,blacklist_policy FROM chat.project_settings WHERE project_id=$1", project).Scan(&enabled, &mode); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	if mode != "reject" {
		return moderation.ErrContentRejected
	}
	var rejected bool
	err := tx.QueryRow(ctx, "SELECT true FROM chat.blacklist_entries WHERE project_id=$1 AND enabled AND normalized_word=ANY($2::text[]) LIMIT 1", project, words).Scan(&rejected)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return moderation.ErrContentRejected
}
