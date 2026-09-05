// Package repository persists upload lifecycle metadata with tenant and actor checks.
package repository

import (
	"context"
	"errors"

	"github.com/F1reStyLe/AlUrMessenger/internal/attachment"
	"github.com/F1reStyLe/AlUrMessenger/internal/auth"
	"github.com/F1reStyLe/AlUrMessenger/internal/identity"
	"github.com/F1reStyLe/AlUrMessenger/internal/policy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ DB *pgxpool.Pool }

const projection = `SELECT id::text,original_name,mime_type,size,width,height,status,created_at,expires_at
 FROM chat.attachments WHERE project_id=$1 AND id=$2`

func scan(row pgx.Row) (attachment.Attachment, error) {
	var result attachment.Attachment
	err := row.Scan(&result.ID, &result.OriginalName, &result.MIME, &result.Size, &result.Width, &result.Height, &result.Status, &result.CreatedAt, &result.ExpiresAt)
	return result, err
}

// uploadPolicy is called before reading a large request and again under the
// Project lock immediately before lifecycle rows are committed.
func uploadPolicy(ctx context.Context, row pgx.Row, actor identity.Actor, global int64) (int64, error) {
	var active, allowImages, allowBots, banned bool
	var kind string
	var projectLimit int64
	err := row.Scan(&active, &allowImages, &allowBots, &projectLimit, &kind, &banned)
	if errors.Is(err, pgx.ErrNoRows) || !active {
		return 0, auth.ErrUnauthenticated
	}
	if err != nil {
		return 0, err
	}
	if banned {
		return 0, identity.ErrBanned
	}
	if kind != "human" && kind != "bot" {
		return 0, policy.ErrForbidden
	}
	if kind == "bot" && !allowBots {
		return 0, policy.ErrFeatureDisabled
	}
	if !allowImages {
		return 0, policy.ErrFeatureDisabled
	}
	return min(projectLimit, global), nil
}

func (s *Store) Limit(ctx context.Context, actor identity.Actor, global int64) (int64, error) {
	return uploadPolicy(ctx, s.DB.QueryRow(ctx, `SELECT p.status='active',COALESCE((s.flags->>'allow_images')::boolean,false),COALESCE((s.flags->>'allow_bots')::boolean,false),s.max_upload_size,u.kind,u.banned_at IS NOT NULL
 FROM chat.projects p JOIN chat.project_settings s ON s.project_id=p.id JOIN chat.users u ON u.project_id=p.id
 WHERE p.id=$1 AND u.id=$2`, actor.ProjectID, actor.User.ID), actor, global)
}

// Begin records a recoverable uploading state before any external object write.
func (s *Store) Begin(ctx context.Context, actor identity.Actor, p attachment.Prepared, global int64) (attachment.Attachment, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return attachment.Attachment{}, err
	}
	defer tx.Rollback(ctx)
	limit, err := uploadPolicy(ctx, tx.QueryRow(ctx, `SELECT p.status='active',COALESCE((s.flags->>'allow_images')::boolean,false),COALESCE((s.flags->>'allow_bots')::boolean,false),s.max_upload_size,u.kind,u.banned_at IS NOT NULL
 FROM chat.projects p JOIN chat.project_settings s ON s.project_id=p.id JOIN chat.users u ON u.project_id=p.id
 WHERE p.id=$1 AND u.id=$2 FOR SHARE OF p,u`, actor.ProjectID, actor.User.ID), actor, global)
	if err != nil {
		return attachment.Attachment{}, err
	}
	if p.Size > limit {
		return attachment.Attachment{}, attachment.ErrTooLarge
	}
	if _, err = tx.Exec(ctx, `INSERT INTO chat.storage_objects(id,project_id,storage_key,mime_type,size,sha256,status)
 VALUES($1,$2,$3,$4,$5,$6,'uploading')`, p.ObjectID, actor.ProjectID, p.StorageKey, p.MIME, p.Size, p.SHA256[:]); err != nil {
		return attachment.Attachment{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO chat.attachments(id,project_id,uploader_id,object_id,original_name,mime_type,size,width,height,status)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'uploading')`, p.AttachmentID, actor.ProjectID, actor.User.ID, p.ObjectID, p.Name, p.MIME, p.Size, p.Width, p.Height); err != nil {
		return attachment.Attachment{}, err
	}
	result, err := scan(tx.QueryRow(ctx, projection, actor.ProjectID, p.AttachmentID))
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

// Ready transitions both rows atomically after MinIO acknowledged the complete object.
func (s *Store) Ready(ctx context.Context, actor identity.Actor, id string) (attachment.Attachment, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return attachment.Attachment{}, err
	}
	defer tx.Rollback(ctx)
	var object string
	err = tx.QueryRow(ctx, "SELECT object_id::text FROM chat.attachments WHERE project_id=$1 AND id=$2 AND uploader_id=$3 AND status='uploading' FOR UPDATE", actor.ProjectID, id, actor.User.ID).Scan(&object)
	if errors.Is(err, pgx.ErrNoRows) {
		return attachment.Attachment{}, attachment.ErrLifecycle
	}
	if err != nil {
		return attachment.Attachment{}, err
	}
	tag, err := tx.Exec(ctx, "UPDATE chat.storage_objects SET status='ready',updated_at=clock_timestamp() WHERE project_id=$1 AND id=$2 AND status='uploading'", actor.ProjectID, object)
	if err != nil || tag.RowsAffected() != 1 {
		return attachment.Attachment{}, attachment.ErrLifecycle
	}
	if _, err = tx.Exec(ctx, "UPDATE chat.attachments SET status='ready' WHERE project_id=$1 AND id=$2", actor.ProjectID, id); err != nil {
		return attachment.Attachment{}, err
	}
	result, err := scan(tx.QueryRow(ctx, projection, actor.ProjectID, id))
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
