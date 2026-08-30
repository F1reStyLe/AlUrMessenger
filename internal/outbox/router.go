package outbox

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/internal/event"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/scram"
)

// HintChannel carries only routing IDs. API instances always read authorized PG events.
const HintChannel = "alur:conversation-hints:v1"
const routerGroup = "chat-router-v1"

// Router bridges durable Kafka records to disposable Redis wakeups. A duplicate
// hint is harmless; an inbox row means publication was attempted successfully.
type Router struct {
	DB    *pgxpool.Pool
	Redis *redis.Client
	Kafka *kgo.Client
}

// NewRouter uses an independent consumer client, sharing producer security policy.
func NewRouter(cfg config.Kafka, db *pgxpool.Pool, r *redis.Client) (*Router, error) {
	opts := []kgo.Opt{kgo.SeedBrokers(cfg.Brokers...), kgo.ConsumerGroup(routerGroup), kgo.ConsumeTopics(Topic), kgo.DisableAutoCommit(), kgo.BlockRebalanceOnPoll(), kgo.FetchMaxBytes(1 << 20), kgo.DialTimeout(3 * time.Second)}
	if cfg.SecurityProtocol == "SASL_SSL" {
		opts = append(opts, kgo.DialTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}), kgo.SASL(scram.Auth{User: cfg.Username, Pass: cfg.Password}.AsSha256Mechanism()))
	}
	c, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, err
	}
	return &Router{DB: db, Redis: r, Kafka: c}, nil
}

// Handle validates the reference against PostgreSQL, so malformed/injected records
// never produce a content event. Failed Redis publication does not commit the inbox.
func (r *Router) Handle(ctx context.Context, key string, data []byte) error {
	var e event.Envelope
	if len(data) > 131072 || json.Unmarshal(data, &e) != nil || e.Version != 1 || e.AggregateID != key {
		return errors.New("EVENT_INVALID")
	}
	for _, id := range []string{e.ID, e.ProjectID, e.AggregateID} {
		v, err := uuid.Parse(id)
		if err != nil || v == uuid.Nil || id != v.String() {
			return errors.New("EVENT_INVALID")
		}
	}
	tx, err := r.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", routerGroup+":"+e.ID); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM chat.consumer_inbox WHERE consumer=$1 AND event_id=$2)", routerGroup, e.ID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return tx.Commit(ctx)
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM chat.conversation_events WHERE project_id=$1 AND conversation_id=$2 AND sequence=$3 AND id=$4 AND envelope=$5::jsonb)", e.ProjectID, e.AggregateID, e.Sequence, e.ID, data).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errors.New("EVENT_INVALID")
	}
	if err = r.Redis.Publish(ctx, HintChannel, e.ProjectID+":"+e.AggregateID).Err(); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO chat.consumer_inbox(consumer,event_id) VALUES($1,$2)", routerGroup, e.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Run never commits beyond a failed record. Retries keep partition order and are
// bounded by shutdown; PostgreSQL polling makes a blocked hint stream nonfatal to WS.
func (r *Router) Run(ctx context.Context) {
	defer r.Kafka.Close()
	for ctx.Err() == nil {
		fetches := r.Kafka.PollRecords(ctx, 32)
		if ctx.Err() != nil {
			r.Kafka.AllowRebalance()
			return
		}
		for _, record := range fetches.Records() {
			for ctx.Err() == nil {
				bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
				err := r.Handle(bounded, string(record.Key), record.Value)
				cancel()
				if err == nil {
					if r.Kafka.CommitRecords(ctx, record) == nil {
						break
					}
				}
				timer := time.NewTimer(time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
				case <-timer.C:
				}
			}
		}
		r.Kafka.AllowRebalance()
		if len(fetches.Errors()) > 0 {
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}
}
