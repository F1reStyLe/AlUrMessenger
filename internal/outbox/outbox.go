// Package outbox publishes committed reference events in per-conversation order.
package outbox

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Topic is provisioned by an operator job, never auto-created by HTTP requests.
const Topic = "chat.events.v1"

// Publisher returns only after broker acknowledgement or a bounded failure.
type Publisher interface {
	Publish(context.Context, string, []byte) error
}

// Kafka uses the shared idempotent producer configured with acknowledgements=all.
type Kafka struct{ Client *kgo.Client }

func (k Kafka) Publish(ctx context.Context, key string, data []byte) error {
	return k.Client.ProduceSync(ctx, &kgo.Record{Topic: Topic, Key: []byte(key), Value: data}).FirstErr()
}

// Worker deliberately keeps publication state in PostgreSQL, not process memory.
type Worker struct {
	DB        *pgxpool.Pool
	Publisher Publisher
	Logger    *slog.Logger
}

// One locks an aggregate and only publishes its earliest pending event. The DB
// transaction spans the bounded broker ack: crash after ack before commit replays
// the same event_id. Consumers must deduplicate; distributed exactly-once is not claimed.
func (w *Worker) One(ctx context.Context, project, conversation string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var locked bool
	if err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))", "outbox:"+project+":"+conversation).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	var id string
	var data []byte
	var attempts int
	var ready bool
	// Retry deadlines are written with the database clock. Evaluate them there as
	// well: worker clock skew must neither skip backoff nor delay an eligible head.
	err = tx.QueryRow(ctx, "SELECT event_id::text,envelope,attempts,next_attempt_at<=clock_timestamp() FROM chat.outbox_events WHERE project_id=$1 AND conversation_id=$2 AND published_at IS NULL ORDER BY sequence LIMIT 1 FOR UPDATE", project, conversation).Scan(&id, &data, &attempts, &ready)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !ready {
		return false, nil
	}
	publishCtx, stop := context.WithTimeout(ctx, 3*time.Second)
	err = w.Publisher.Publish(publishCtx, conversation, data)
	stop()
	if err != nil {
		// Do not advance past a failed head. Other conversations remain eligible.
		backoff := min(60, 1<<min(attempts, 6))
		_, updateErr := tx.Exec(ctx, "UPDATE chat.outbox_events SET attempts=attempts+1,next_attempt_at=clock_timestamp()+$2*interval '1 second',last_error_code='PUBLISH_FAILED' WHERE event_id=$1", id, backoff)
		if updateErr != nil {
			return false, updateErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return false, commitErr
		}
		return false, errors.New("OUTBOX_PUBLISH_FAILED")
	}
	if _, err = tx.Exec(ctx, "UPDATE chat.outbox_events SET published_at=clock_timestamp(),attempts=attempts+1,last_error_code=NULL WHERE event_id=$1", id); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// Batch chooses only ready heads. The second check inside One makes multiple
// worker instances safe even if they selected the same candidate concurrently.
func (w *Worker) Batch(ctx context.Context) (bool, error) {
	rows, err := w.DB.Query(ctx, `SELECT o.project_id::text,o.conversation_id::text FROM chat.outbox_events o
 WHERE o.published_at IS NULL AND o.next_attempt_at<=clock_timestamp()
 AND NOT EXISTS(SELECT 1 FROM chat.outbox_events head WHERE head.project_id=o.project_id AND head.conversation_id=o.conversation_id AND head.published_at IS NULL AND head.sequence<o.sequence)
 ORDER BY o.next_attempt_at,o.event_id LIMIT 32`)
	if err != nil {
		return false, err
	}
	type candidate struct{ project, conversation string }
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.project, &c.conversation); err != nil {
			rows.Close()
			return false, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	progress := false
	for _, c := range candidates {
		if ctx.Err() != nil {
			return progress, ctx.Err()
		}
		ok, e := w.One(ctx, c.project, c.conversation)
		progress = progress || ok
		if e != nil && w.Logger != nil {
			w.Logger.Warn("outbox event remains pending", "error_code", "OUTBOX_PUBLISH_FAILED")
		}
	}
	return progress, nil
}

// Run drains available work promptly and waits only when idle/failing. Cancellation
// reaches the active publish/SQL transaction; uncommitted work remains for restart.
func (w *Worker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		progress, err := w.Batch(ctx)
		if err != nil && w.Logger != nil && ctx.Err() == nil {
			w.Logger.Warn("outbox query failed", "error_code", "OUTBOX_UNAVAILABLE")
		}
		if progress && err == nil {
			continue
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
