package app

import (
	"bytes"
	"context"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
)

// testEnvironment: Изолирует все поддерживаемые настройки от environment разработчика; t.Setenv восстанавливает их после теста.
func testEnvironment(t *testing.T) {
	t.Helper()
	// Не наследуем secret-file overrides разработчика при подстановке unit credentials.
	for _, name := range []string{"POSTGRES_URL", "REDIS_URL", "MINIO_ACCESS_KEY", "MINIO_SECRET_KEY"} {
		t.Setenv(name+"_FILE", "")
		_ = os.Unsetenv(name + "_FILE")
	}
	for key, value := range map[string]string{
		"APP_ENV": "test", "APP_SHUTDOWN_TIMEOUT": "1s", "LOG_LEVEL": "info", "LOG_FORMAT": "json",
		"HTTP_ADDR": "127.0.0.1:8080", "HTTP_READ_HEADER_TIMEOUT": "1s", "HTTP_READ_TIMEOUT": "2s",
		"HTTP_WRITE_TIMEOUT": "2s", "HTTP_IDLE_TIMEOUT": "1s", "HTTP_MAX_HEADER_BYTES": "32768", "HTTP_MAX_BODY_BYTES": "1024",
		"POSTGRES_URL": "postgres://test:test@127.0.0.1:5432/test?sslmode=disable", "POSTGRES_MAX_CONNS": "2",
		"REDIS_URL": "redis://127.0.0.1:6379/0", "KAFKA_BROKERS": "127.0.0.1:9092", "KAFKA_SECURITY_PROTOCOL": "PLAINTEXT",
		"MINIO_ENDPOINT": "http://127.0.0.1:9000", "MINIO_PUBLIC_ENDPOINT": "http://127.0.0.1:9000", "MINIO_BUCKET": "chat-attachments", "MINIO_REGION": "us-east-1",
		"MINIO_ACCESS_KEY": "unit-test", "MINIO_SECRET_KEY": "unit-test-secret", "INFRA_TIMEOUT": "1s",
		"CORS_ALLOWED_ORIGINS": "", "RATE_IP_PER_MINUTE": "120", "RATE_USER_PER_MINUTE": "60",
		"AUTH_MODE": "dev-rsa", "AUTH_BASE_URL": "", "AUTH_PROJECT_ID": "", "AUTH_PUBLIC_KEY_FILE": "",
	} {
		t.Setenv(key, value)
	}
}

// TestAuthFailsBeforeInfrastructure verifies fail-fast without connecting to fake DB credentials.
func TestAuthFailsBeforeInfrastructure(t *testing.T) {
	testEnvironment(t)
	t.Setenv("AUTH_PUBLIC_KEY_FILE", "not-a-key-secret-path")
	var output bytes.Buffer
	if run(t.Context(), config.API, &output) != 1 || !strings.Contains(output.String(), "authentication configuration rejected") || strings.Contains(output.String(), "not-a-key-secret-path") {
		t.Fatal("auth startup failed unsafely")
	}
}

// TestConfigurationFailureIsNonzeroAndRedacted: Проверяет публичный exit status и полезную диагностику без исходного секретного значения.
func TestConfigurationFailureIsNonzeroAndRedacted(t *testing.T) {
	testEnvironment(t)
	t.Setenv("APP_ENV", "secret-environment")
	var output bytes.Buffer
	if code := run(t.Context(), config.API, &output); code != 1 {
		t.Fatalf("exit=%d, want 1", code)
	}
	if strings.Contains(output.String(), "secret-environment") || !strings.Contains(output.String(), "APP_ENV") {
		t.Fatalf("unsafe/unhelpful startup diagnostic: %s", output.String())
	}
}

// TestBindFailureIsNonzero: Занимает реальный ephemeral port, чтобы проверить ошибку bind без привязки к портам разработчика.
func TestBindFailureIsNonzero(t *testing.T) {
	testEnvironment(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("HTTP_ADDR", listener.Addr().String())
	var output bytes.Buffer
	if code := run(t.Context(), config.Worker, &output); code != 1 {
		t.Fatalf("exit=%d, want 1", code)
	}
	if !strings.Contains(output.String(), "LISTEN_FAILED") {
		t.Fatal("missing structured bind failure")
	}
}

// TestCancelledStartupDoesNotBind: Отменяет context до запуска: процесс не должен сообщать о старте или возвращать ошибку.
func TestCancelledStartupDoesNotBind(t *testing.T) {
	testEnvironment(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var output bytes.Buffer
	if code := run(ctx, config.API, &output); code != 0 {
		t.Fatalf("exit=%d, want 0", code)
	}
	if output.Len() != 0 {
		t.Fatal("cancelled startup should not announce listening")
	}
}
