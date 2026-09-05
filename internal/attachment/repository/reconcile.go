package repository

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ObjectRemover is deliberately narrower than the upload object store. Cleanup
// receives only an exact key selected under a DB lock and cannot list a bucket.
type ObjectRemover interface {
	Delete(context.Context, string) error
}

// Cleaner reconciles crash-left uploading objects and expired unattached ready
// uploads. attached references always win: an object shared by a forward cannot
// be scheduled until every logical attachment is terminal.
type Cleaner struct {
	DB     *pgxpool.Pool
	Blobs  ObjectRemover
	Logger *slog.Logger
}

// One claims at most one object. pending_delete is durable retry state, so a
// crash before or after an idempotent MinIO delete is safe.
func (c *Cleaner) One(ctx context.Context) (bool, error) {
	tx, err := c.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var project, object, key string
	err = tx.QueryRow(ctx, `SELECT o.project_id::text,o.id::text,o.storage_key
 FROM chat.storage_objects o
 WHERE o.status='pending_delete' OR (
   o.status IN ('uploading','ready')
   AND NOT EXISTS (SELECT 1 FROM chat.attachments a WHERE a.project_id=o.project_id AND a.object_id=o.id AND a.status='attached')
   AND (o.created_at<clock_timestamp()-interval '1 hour' OR EXISTS (
     SELECT 1 FROM chat.attachments a WHERE a.project_id=o.project_id AND a.object_id=o.id AND a.status='ready' AND a.expires_at<=clock_timestamp()
   ))
 )
 ORDER BY o.created_at,o.id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&project, &object, &key)
	if err == pgx.ErrNoRows {
		return false, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, "UPDATE chat.attachments SET status='deleted' WHERE project_id=$1 AND object_id=$2 AND status IN ('uploading','ready')", project, object); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, "UPDATE chat.storage_objects SET status='pending_delete',updated_at=clock_timestamp() WHERE project_id=$1 AND id=$2", project, object); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	if err = c.Blobs.Delete(ctx, key); err != nil {
		return true, err
	}
	_, err = c.DB.Exec(ctx, "UPDATE chat.storage_objects SET status='deleted',updated_at=clock_timestamp() WHERE project_id=$1 AND id=$2 AND status='pending_delete'", project, object)
	return true, err
}

func (c *Cleaner) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		worked, err := c.One(ctx)
		if err != nil && ctx.Err() == nil {
			c.Logger.Error("attachment reconciliation failed", "error_code", "ATTACHMENT_RECONCILE_FAILED")
		}
		if worked && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
