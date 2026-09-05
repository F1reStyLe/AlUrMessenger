//go:build integration

// Package integration проверяет реальные isolated services; запускать через fixture script.
package integration

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/httpserver"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/infrastructure"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/migration"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/objectstore"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/postgres"
	"github.com/F1reStyLe/AlUrMessenger/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// eventually допускает ограниченный cold startup/reconnect без бесконечных sleep.
func eventually(t *testing.T, check func(context.Context) bool) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		ok := check(ctx)
		cancel()
		if ok {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("dependency did not reach the expected state before deadline")
}

// fixture управляет только services собственного уникального Compose project.
func fixture(t *testing.T, args ...string) {
	t.Helper()
	project := os.Getenv("ALUR_TEST_PROJECT")
	if !strings.HasPrefix(project, "alurinfra-") {
		t.Fatal("unsafe fixture project")
	}
	file, err := filepath.Abs("compose.yaml")
	if err != nil {
		t.Fatal("fixture path unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose", "-f", file, "-p", project}, args...)...)
	if err := cmd.Run(); err != nil {
		t.Fatal("fixture operation failed")
	}
}

// TestInfrastructureLifecycle покрывает SQL, Redis, Kafka и private S3 реальными
// операциями, затем сбои/восстановление через тот же HTTP middleware, что у API.
func TestInfrastructureLifecycle(t *testing.T) {
	if os.Getenv("ALUR_INTEGRATION_TEST") != "1" {
		t.Skip("use scripts/test-infrastructure.ps1; external databases are not test targets")
	}
	cfg, err := config.LoadInfrastructure("test")
	if err != nil {
		t.Fatal(err)
	}
	pg, err := postgres.Open(t.Context(), cfg.Postgres, cfg.Timeout)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	eventually(t, func(ctx context.Context) bool { return pg.Ping(ctx) == nil })
	if postgres.Check(t.Context(), pg) == nil {
		t.Fatal("unmigrated database reported ready")
	}
	// Два независимых provider конкурируют за advisory lock; повтор up — no-op.
	migrationURL := os.Getenv("MIGRATION_POSTGRES_URL")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := migration.Run(t.Context(), migrationURL, "up"); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if version, err := migration.Run(t.Context(), migrationURL, "up"); err != nil || version != migrations.Version {
		t.Fatal("migration repeat failed")
	}
	if err := postgres.Check(t.Context(), pg); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(t.Context(), "CREATE TABLE chat.forbidden(id int)"); err == nil {
		t.Fatal("runtime can execute DDL")
	}
	if _, err := pg.Exec(t.Context(), "CREATE TABLE public.forbidden(id int)"); err == nil {
		t.Fatal("runtime can execute public DDL")
	}
	if _, err := pg.Exec(t.Context(), "UPDATE public.goose_db_version SET version_id=999 WHERE version_id=1"); err == nil {
		t.Fatal("runtime can alter migration history")
	}
	// Проверяем несовместимую будущую версию и восстановление, без изменения истории Git/SQL.
	migrator, err := pgx.Connect(t.Context(), migrationURL)
	if err != nil {
		t.Fatal("migration connection failed")
	}
	defer migrator.Close(context.Background())
	if _, err = migrator.Exec(t.Context(), "UPDATE public.goose_db_version SET version_id=999 WHERE version_id=1"); err != nil {
		t.Fatal("schema test setup failed")
	}
	if postgres.Check(t.Context(), pg) == nil {
		t.Fatal("future schema version accepted")
	}
	if _, err = migrator.Exec(t.Context(), "UPDATE public.goose_db_version SET version_id=1 WHERE version_id=999"); err != nil {
		t.Fatal("schema restore failed")
	}
	adminCfg := cfg.Storage
	t.Run("identity", func(t *testing.T) { testIdentity(t, pg, migrator) })
	t.Run("policies", func(t *testing.T) { testPolicies(t, pg, migrator, cfg.RedisURL) })
	t.Run("local-roles", func(t *testing.T) { testLocalRoles(t, pg, migrator) })
	t.Run("conversations", func(t *testing.T) { testConversations(t, pg, migrator, cfg.RedisURL) })
	t.Run("messages", func(t *testing.T) { testMessages(t, pg, migrator) })
	t.Run("message-features", func(t *testing.T) { testMessageFeatures(t, pg, migrator) })
	t.Run("reactions-pins", func(t *testing.T) { testRelations(t, pg, migrator) })
	t.Run("forward", func(t *testing.T) { testForward(t, pg, migrator) })
	t.Run("phase5-audit", func(t *testing.T) { testPhase5Audit(t, pg, migrator) })
	adminCfg.AccessKey = "test-root"
	adminCfg.SecretKey = os.Getenv("ALUR_TEST_MINIO_ADMIN")
	admin, err := objectstore.Open(adminCfg, cfg.Timeout)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	eventually(t, func(ctx context.Context) bool {
		_, err := admin.Client.BucketExists(ctx, admin.Bucket)
		return err == nil
	})
	if admin.Check(t.Context()) == nil {
		t.Fatal("missing bucket accepted")
	}
	for range 2 {
		if err = admin.Initialize(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	var clients *infrastructure.Clients
	eventually(t, func(ctx context.Context) bool { clients, err = infrastructure.Open(ctx, cfg); return err == nil })
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if clients.Close(ctx) != nil {
			t.Error("client shutdown timed out")
		}
	})
	// Redis round-trip с TTL не подменяется PING; очищается только тестовый ключ.
	if err = clients.Redis.Set(t.Context(), "fixture:key", "value", time.Minute).Err(); err != nil {
		t.Fatal("redis write failed")
	}
	if v, err := clients.Redis.Get(t.Context(), "fixture:key").Result(); err != nil || v != "value" {
		t.Fatal("redis read failed")
	}
	if ttl := clients.Redis.TTL(t.Context(), "fixture:key").Val(); ttl <= 0 {
		t.Fatal("redis TTL missing")
	}
	// Topic создаётся только тестом; production adapter не включает auto-create.
	req := kmsg.NewPtrCreateTopicsRequest()
	topic := kmsg.NewCreateTopicsRequestTopic()
	topic.Topic = "fixture.events"
	topic.NumPartitions = 1
	topic.ReplicationFactor = 1
	req.Topics = []kmsg.CreateTopicsRequestTopic{topic}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	response, err := req.RequestWith(ctx, clients.Kafka)
	if err != nil || len(response.Topics) != 1 || response.Topics[0].ErrorCode != 0 {
		t.Fatal("kafka topic creation failed")
	}
	if err = clients.Kafka.ProduceSync(ctx, &kgo.Record{Topic: topic.Topic, Key: []byte("fixture"), Value: []byte("event")}).FirstErr(); err != nil {
		t.Fatal("kafka acknowledged produce failed")
	}
	consumer, err := kgo.NewClient(kgo.SeedBrokers(cfg.Kafka.Brokers...), kgo.ConsumeTopics(topic.Topic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal("consumer config failed")
	}
	defer consumer.Close()
	records := consumer.PollFetches(ctx).Records()
	if len(records) != 1 || string(records[0].Value) != "event" {
		t.Fatal("kafka consume failed")
	}
	// Private object: авторизованный round-trip проходит, anonymous GET запрещён.
	t.Run("event-routing", func(t *testing.T) { testEventRouting(t, clients, cfg) })
	t.Run("attachment-upload", func(t *testing.T) { testAttachmentUpload(t, clients, migrator) })
	t.Run("realtime", func(t *testing.T) { testRealtime(t, clients, migrator) })
	// Storage owns a fresh deadline: the Kafka budget must not expire while the
	// independent realtime scenario exercises reconnect, expiry and Redis outages.
	objectCtx, cancelObjects := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancelObjects()
	key := "fixture/object.txt"
	if _, err = clients.Storage.Client.PutObject(objectCtx, clients.Storage.Bucket, key, strings.NewReader("private"), 7, minio.PutObjectOptions{}); err != nil {
		t.Fatal("private object write failed")
	}
	object, err := clients.Storage.Client.GetObject(objectCtx, clients.Storage.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		t.Fatal("private object read failed")
	}
	data, err := io.ReadAll(object)
	_ = object.Close()
	if err != nil || string(data) != "private" {
		t.Fatal("private object content mismatch")
	}
	request, _ := http.NewRequestWithContext(objectCtx, "GET", os.Getenv("MINIO_ENDPOINT")+"/"+clients.Storage.Bucket+"/"+key, nil)
	res, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal("anonymous access check failed")
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("private object allows anonymous read")
	}
	// Runtime IAM запрещает изменение bucket policy, даже на безопасное пустое значение.
	if err = clients.Storage.Client.SetBucketPolicy(objectCtx, clients.Storage.Bucket, ""); err == nil {
		t.Fatal("runtime can change bucket policy")
	}
	var logs bytes.Buffer
	srv := httpserver.New(config.HTTP{MaxBodyBytes: 1024, MaxHeaderBytes: 32768, ReadHeaderTimeout: time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: time.Second}, 5*time.Second, slog.New(slog.NewJSONHandler(&logs, nil)), clients.Check)
	// Настоящий Server.Run нужен, чтобы проверить публичные probes и штатный drain.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("HTTP bind failed")
	}
	runCtx, stop := context.WithCancel(t.Context())
	defer stop()
	done := make(chan error, 1)
	go func() { done <- srv.Run(runCtx, ln) }()
	base := "http://" + ln.Addr().String()
	// Собственный transport и чтение до EOF исключают незавершённые клиентские
	// соединения из теста drain; default transport других S3/HTTP проверок не затрагиваем.
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := http.Client{Timeout: 4 * time.Second, Transport: transport}
	probe := func(path string) int {
		res, err := client.Get(base + path)
		if err != nil {
			return 0
		}
		defer res.Body.Close()
		_, _ = io.Copy(io.Discard, res.Body)
		return res.StatusCode
	}
	if probe("/health/ready") != 200 {
		t.Fatal("healthy dependencies not ready")
	}
	// Публичная policy: Check/Initialize обязаны отказаться, не замаскировать проблему reset-ом.
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::chat-attachments/*"]}]}`
	if err = admin.Client.SetBucketPolicy(t.Context(), admin.Bucket, policy); err != nil {
		t.Fatal("policy test setup failed")
	}
	if admin.Initialize(t.Context()) == nil || probe("/health/ready") != 503 {
		t.Fatal("public bucket was accepted")
	}
	if err = admin.Client.SetBucketPolicy(t.Context(), admin.Bucket, ""); err != nil {
		t.Fatal("policy restore failed")
	}
	for _, service := range []string{"postgres", "redis", "kafka", "minio"} {
		t.Run("outage_"+service, func(t *testing.T) {
			fixture(t, "stop", service)
			defer fixture(t, "start", service)
			if probe("/health/live") != 200 || probe("/health/ready") != 503 {
				t.Fatal("dependency loss did not affect readiness independently of liveness")
			}
			fixture(t, "start", service)
			eventually(t, func(context.Context) bool { return probe("/health/ready") == 200 })
		})
	}
	transport.CloseIdleConnections()
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("HTTP drain failed")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("HTTP drain hung")
	}
	for _, secret := range []string{cfg.Postgres.URL, cfg.RedisURL, cfg.Storage.SecretKey, adminCfg.SecretKey} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("secret appeared in logs")
		}
	}
}
