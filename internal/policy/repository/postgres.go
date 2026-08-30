// Package repository implements policy writes and audit in one PostgreSQL transaction.
package repository

import (
	"context"
	"encoding/json"
	"github.com/F1reStyLe/AlUrMessenger/internal/auth"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store uses only the restricted runtime pool; no migration credentials in HTTP.
type Store struct{ DB *pgxpool.Pool }

// projection joins the name without exposing authentication bindings through admin DTO.
const projection = `SELECT s.project_id::text,p.name,s.flags,s.max_upload_size,s.message_retention_days,s.settings_version FROM chat.project_settings s JOIN chat.projects p ON p.id=s.project_id WHERE s.project_id=$1`

// scan validates JSON decoding; corrupt settings fail closed instead of applying defaults.
func scan(row pgx.Row) (policy.Settings, error) {
	var s policy.Settings
	err := row.Scan(&s.ProjectID, &s.Name, &s.Flags, &s.MaxUploadSize, &s.RetentionDays, &s.Version)
	return s, err
}

// Get always scopes the settings snapshot by trusted Project ID.
func (s *Store) Get(ctx context.Context, project string) (policy.Settings, error) {
	return scan(s.DB.QueryRow(ctx, projection, project))
}

// Update takes an exclusive Project lock before user/settings locks. Profile writes
// take SHARE on the same Project, so policy transitions serialize with protected writes.
func (s *Store) Update(ctx context.Context, a identity.Actor, p policy.Patch, tr policy.Trace) (policy.Settings, error) {
	if !a.Admin {
		return policy.Settings{}, policy.ErrForbidden
	}
	if err := p.Validate(); err != nil {
		return policy.Settings{}, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return policy.Settings{}, err
	}
	defer tx.Rollback(ctx)
	var active, banned, admin bool
	if err = tx.QueryRow(ctx, "SELECT status='active' FROM chat.projects WHERE id=$1 FOR UPDATE", a.ProjectID).Scan(&active); err != nil {
		return policy.Settings{}, err
	}
	if !active {
		return policy.Settings{}, auth.ErrUnauthenticated
	}
	if err = tx.QueryRow(ctx, "SELECT banned_at IS NOT NULL,role='admin' FROM chat.users WHERE project_id=$1 AND id=$2 FOR SHARE", a.ProjectID, a.User.ID).Scan(&banned, &admin); err != nil {
		return policy.Settings{}, err
	}
	if banned {
		return policy.Settings{}, identity.ErrBanned
	}
	// Recheck the local role under lock so a concurrent demotion cannot authorize a write.
	if !admin {
		return policy.Settings{}, policy.ErrForbidden
	}
	current, err := scan(tx.QueryRow(ctx, projection, a.ProjectID))
	if err != nil {
		return current, err
	}
	if current.Version != p.ExpectedVersion {
		return current, policy.ErrConflict
	}
	for k, v := range p.Flags {
		current.Flags[k] = *v
	}
	if p.MaxUploadSize != nil {
		current.MaxUploadSize = *p.MaxUploadSize
	}
	if p.RetentionDays != nil {
		current.RetentionDays = *p.RetentionDays
	}
	flags, err := json.Marshal(current.Flags)
	if err != nil {
		return current, err
	}
	if err = tx.QueryRow(ctx, `UPDATE chat.project_settings SET flags=$2,max_upload_size=$3,message_retention_days=$4,settings_version=settings_version+1,updated_by=$5,updated_at=now() WHERE project_id=$1 RETURNING settings_version`, a.ProjectID, flags, current.MaxUploadSize, current.RetentionDays, a.User.ID).Scan(&current.Version); err != nil {
		return current, err
	}
	if _, err = tx.Exec(ctx, "UPDATE chat.projects SET policy_version=policy_version+1,updated_at=now() WHERE id=$1", a.ProjectID); err != nil {
		return current, err
	}
	// UUID v7 supports forward keyset browsing. Ordering is ID order, not commit order.
	id, err := uuid.NewV7()
	if err != nil {
		return current, err
	}
	metadata, err := json.Marshal(map[string]any{"changed_fields": p.Fields()})
	if err != nil {
		return current, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO chat.audit_logs(id,project_id,actor_id,actor_type,action,resource_type,resource_id,metadata,ip,request_id) VALUES($1,$2,$3,'user','project.settings.updated','project',$2,$4,NULLIF($5,'')::inet,$6)`, id.String(), a.ProjectID, a.User.ID, metadata, tr.IP, tr.RequestID); err != nil {
		return current, err
	}
	return current, tx.Commit(ctx)
}

// Audit uses tenant-qualified forward UUID keyset; event metadata never contains content.
func (s *Store) Audit(ctx context.Context, project, after string, limit int) ([]policy.Audit, error) {
	if after == "" {
		after = uuid.Nil.String()
	}
	rows, err := s.DB.Query(ctx, `SELECT id::text,actor_id::text,action,resource_type,resource_id::text,metadata,request_id::text,created_at FROM chat.audit_logs WHERE project_id=$1 AND id>$2 ORDER BY id LIMIT $3`, project, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []policy.Audit{}
	for rows.Next() {
		var e policy.Audit
		if err = rows.Scan(&e.ID, &e.ActorID, &e.Action, &e.ResourceType, &e.ResourceID, &e.Metadata, &e.RequestID, &e.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}
