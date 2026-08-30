// Package infrastructure собирает сетевые adapters и владеет их lifecycle.
// Здесь нет бизнес-jobs, автоматических миграций или создания Kafka topics.
package infrastructure

import (
	"context"
	"crypto/tls"
	"errors"
	"sync"

	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/objectstore"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/scram"
)

// Clients объединяет ресурсы процесса, доступные будущим repositories/adapters.
// Успешный Open передаёт владельцу обязанность Close после drain HTTP/jobs.
type Clients struct {
	Postgres *pgxpool.Pool
	Redis    *redis.Client
	Kafka    *kgo.Client
	Storage  *objectstore.Store
	once     sync.Once
	closed   chan struct{}
}

// Open создаёт клиентов и проверяет обязательные зависимости до обслуживания HTTP.
// При частичной ошибке освобождает уже созданные ресурсы; наружу выходят только коды.
func Open(ctx context.Context, cfg config.Infrastructure) (*Clients, error) {
	c := &Clients{closed: make(chan struct{})}
	var err error
	defer func() {
		if err != nil {
			c.close()
		}
	}()
	c.Postgres, err = postgres.Open(ctx, cfg.Postgres, cfg.Timeout)
	if err != nil {
		return nil, err
	}
	ro, parseErr := redis.ParseURL(cfg.RedisURL)
	if parseErr != nil {
		err = errors.New("REDIS_CONFIG_INVALID")
		return nil, err
	}
	ro.DialTimeout = cfg.Timeout
	ro.ReadTimeout = cfg.Timeout
	ro.WriteTimeout = cfg.Timeout
	ro.ContextTimeoutEnabled = true
	ro.MaxRetries = -1
	ro.PoolSize = 10
	ro.MaxActiveConns = 10
	// Отключаем дополнительные identity commands; readiness требует только PING.
	ro.DisableIdentity = true
	c.Redis = redis.NewClient(ro)
	opts := []kgo.Opt{kgo.SeedBrokers(cfg.Kafka.Brokers...), kgo.DialTimeout(cfg.Timeout), kgo.RequestTimeoutOverhead(cfg.Timeout),
		kgo.ProducerBatchMaxBytes(1 << 20), kgo.MaxBufferedRecords(1000), kgo.RequiredAcks(kgo.AllISRAcks()), kgo.RecordDeliveryTimeout(cfg.Timeout)}
	if cfg.Kafka.SecurityProtocol == "SASL_SSL" {
		opts = append(opts, kgo.DialTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}), kgo.SASL(scram.Auth{User: cfg.Kafka.Username, Pass: cfg.Kafka.Password}.AsSha256Mechanism()))
	}
	c.Kafka, err = kgo.NewClient(opts...)
	if err != nil {
		err = errors.New("KAFKA_CONFIG_INVALID")
		return nil, err
	}
	c.Storage, err = objectstore.Open(cfg.Storage, cfg.Timeout)
	if err != nil {
		return nil, err
	}
	check, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	err = c.Check(check)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Check выполняет четыре независимых проверки параллельно в общем deadline.
// Запросы не создают таблицы/topics/objects. Kafka metadata подтверждает связь с
// broker, но не права будущего producer/consumer или готовность ещё не созданных topics.
// На этапе 1.2 readiness консервативна: любой сбой даёт 503 у обеих ролей.
func (c *Clients) Check(ctx context.Context) error {
	select {
	case <-c.closed:
		return errors.New("INFRASTRUCTURE_CLOSED")
	default:
	}
	checks := []struct {
		code string
		run  func(context.Context) error
	}{
		{"POSTGRES_UNAVAILABLE", func(ctx context.Context) error { return postgres.Check(ctx, c.Postgres) }},
		{"REDIS_UNAVAILABLE", func(ctx context.Context) error { return c.Redis.Ping(ctx).Err() }},
		{"KAFKA_UNAVAILABLE", c.Kafka.Ping},
		{"MINIO_UNAVAILABLE", c.Storage.Check},
	}
	results := make([]error, len(checks))
	var wg sync.WaitGroup
	for i, check := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if check.run(ctx) != nil {
				results[i] = errors.New(check.code)
			}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return errors.New("INFRASTRUCTURE_CHECK_TIMEOUT")
	}
	return errors.Join(results...)
}

// Close ограничивает ожидание освобождения клиентов. При нарушении borrower-ом
// context contract pool.Close может ждать дольше; процесс получает ошибку deadline,
// а не бесконечно зависает. Повторный Close ждёт то же завершение.
func (c *Clients) Close(ctx context.Context) error {
	c.once.Do(func() { go c.close() })
	select {
	case <-c.closed:
		return nil
	case <-ctx.Done():
		return errors.New("INFRASTRUCTURE_CLOSE_TIMEOUT")
	}
}

// close освобождает ресурсы в обратном порядке создания. Kafka.Close останавливает
// служебные goroutines клиента; бизнес-публикации должны завершить flush до этого шага.
func (c *Clients) close() {
	if c.Storage != nil {
		c.Storage.Close()
	}
	if c.Kafka != nil {
		c.Kafka.Close()
	}
	if c.Redis != nil {
		_ = c.Redis.Close()
	}
	if c.Postgres != nil {
		c.Postgres.Close()
	}
	close(c.closed)
}
