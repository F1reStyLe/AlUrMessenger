//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/internal/outbox"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/infrastructure"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// testEventRouting uses actual Kafka acknowledgements, Redis Pub/Sub and a durable inbox.
func testEventRouting(t *testing.T, c *infrastructure.Clients, cfg config.Infrastructure) {
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	req := kmsg.NewPtrCreateTopicsRequest()
	topic := kmsg.NewCreateTopicsRequestTopic()
	topic.Topic = outbox.Topic
	topic.NumPartitions = 1
	topic.ReplicationFactor = 1
	req.Topics = []kmsg.CreateTopicsRequestTopic{topic}
	res, err := req.RequestWith(ctx, c.Kafka)
	if err != nil || len(res.Topics) != 1 || res.Topics[0].ErrorCode != 0 {
		t.Fatal("topic create", err)
	}
	var p, conv string
	if err = c.Postgres.QueryRow(ctx, "SELECT project_id::text,conversation_id::text FROM chat.outbox_events WHERE published_at IS NULL ORDER BY event_id LIMIT 1").Scan(&p, &conv); err != nil {
		t.Fatal(err)
	}
	worker := &outbox.Worker{DB: c.Postgres, Publisher: outbox.Kafka{Client: c.Kafka}}
	if ok, e := worker.One(ctx, p, conv); e != nil || !ok {
		t.Fatal("broker publication", ok, e)
	}
	reader, err := kgo.NewClient(kgo.SeedBrokers(cfg.Kafka.Brokers...), kgo.ConsumeTopics(outbox.Topic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	records := reader.PollFetches(ctx).Records()
	if len(records) != 1 {
		t.Fatal("published event missing", len(records))
	}
	subscription := c.Redis.Subscribe(ctx, outbox.HintChannel)
	defer subscription.Close()
	if _, err = subscription.Receive(ctx); err != nil {
		t.Fatal(err)
	}
	router := &outbox.Router{DB: c.Postgres, Redis: c.Redis}
	for range 2 {
		if err = router.Handle(ctx, string(records[0].Key), records[0].Value); err != nil {
			t.Fatal(err)
		}
	}
	hint, err := subscription.ReceiveMessage(ctx)
	if err != nil || hint.Payload != p+":"+conv {
		t.Fatal("routing hint", err)
	}
	var count int
	if err = c.Postgres.QueryRow(ctx, "SELECT count(*) FROM chat.consumer_inbox").Scan(&count); err != nil || count != 1 {
		t.Fatal("consumer dedup", count, err)
	}
	// Replaying a payload which does not match the canonical event must fail closed.
	if err = router.Handle(ctx, conv, []byte(`{"event_version":1}`)); err == nil {
		t.Fatal("invalid event accepted")
	}
}
