package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// infraEnvironment задаёт полный набор unit settings; не подключается к сервисам.
func infraEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"POSTGRES_URL", "REDIS_URL", "MINIO_ACCESS_KEY", "MINIO_SECRET_KEY", "KAFKA_USERNAME", "KAFKA_PASSWORD"} {
		// t.Setenv регистрирует восстановление, затем удаляем конфликтующий file override.
		t.Setenv(name+"_FILE", "")
		_ = os.Unsetenv(name + "_FILE")
	}
	for k, v := range map[string]string{"POSTGRES_URL": "postgres://runtime:unit-secret@127.0.0.1:5432/chat?sslmode=disable", "POSTGRES_MAX_CONNS": "2",
		"REDIS_URL": "redis://:unit-secret@127.0.0.1:6379/0", "MINIO_ENDPOINT": "http://127.0.0.1:9000", "MINIO_PUBLIC_ENDPOINT": "http://127.0.0.1:9000", "MINIO_REGION": "us-east-1",
		"MINIO_BUCKET": "chat-attachments", "MINIO_ACCESS_KEY": "unit-key", "MINIO_SECRET_KEY": "unit-secret",
		"KAFKA_BROKERS": "127.0.0.1:9092", "KAFKA_SECURITY_PROTOCOL": "PLAINTEXT", "INFRA_TIMEOUT": "1s"} {
		t.Setenv(k, v)
	}
}

// TestInfrastructureRejectsUnsafeSettings фиксирует fail-fast и отсутствие секретов
// в diagnostics, включая попытки отключить проверку TLS через URL query.
func TestInfrastructureRejectsUnsafeSettings(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"POSTGRES_URL", "postgres://user:unit-secret@localhost/db?sslmode=require"},
		{"POSTGRES_URL", "postgres://user:unit-secret@localhost/db?sslmode=disable&sslmode=verify-full"},
		{"POSTGRES_URL", "postgres://user:unit-secret@localhost/db?sslmode=disable&password=unit-secret"},
		{"POSTGRES_MAX_CONNS", "0"}, {"REDIS_URL", "rediss://localhost/0?skip_verify=true"},
		{"KAFKA_BROKERS", "localhost:70000"}, {"KAFKA_SECURITY_PROTOCOL", "SASL_PLAINTEXT"},
		{"MINIO_ENDPOINT", "http://unit-secret@localhost:9000"}, {"MINIO_BUCKET", "public_bucket"},
		{"MINIO_PUBLIC_ENDPOINT", "http://unit-secret@localhost:9000"},
		{"MINIO_SECRET_KEY", ""}, {"INFRA_TIMEOUT", "0s"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			infraEnvironment(t)
			t.Setenv(tc.key, tc.value)
			_, err := LoadInfrastructure("test")
			if err == nil {
				t.Fatal("invalid config accepted")
			}
			if strings.Contains(err.Error(), "unit-secret") {
				t.Fatal("secret leaked")
			}
		})
	}
}

// TestProductionRequiresTLSAndAuthentication проверяет отсутствие downgrade defaults.
// Реальные production certificates/ACL не подменяются этим unit test.
func TestProductionRequiresTLSAndAuthentication(t *testing.T) {
	infraEnvironment(t)
	if _, err := LoadInfrastructure("production"); err == nil {
		t.Fatal("plaintext accepted in production")
	}
	t.Setenv("POSTGRES_URL", "postgres://runtime:unit-secret@localhost/db?sslmode=verify-full")
	t.Setenv("REDIS_URL", "rediss://:unit-secret@localhost:6379/0")
	t.Setenv("MINIO_ENDPOINT", "https://localhost:9000")
	t.Setenv("MINIO_PUBLIC_ENDPOINT", "https://files.example.test")
	t.Setenv("KAFKA_SECURITY_PROTOCOL", "SASL_SSL")
	t.Setenv("KAFKA_USERNAME", "unit-user")
	t.Setenv("KAFKA_PASSWORD", "unit-secret")
	if _, err := LoadInfrastructure("production"); err != nil {
		t.Fatal(err)
	}
}

// TestSecretFiles проверяет разрешённый файл, конфликт источников и ограничение чтения.
func TestSecretFiles(t *testing.T) {
	infraEnvironment(t)
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("file-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINIO_SECRET_KEY_FILE", path)
	if _, err := secret("MINIO_SECRET_KEY"); err == nil {
		t.Fatal("ambiguous secret accepted")
	}
	_ = os.Unsetenv("MINIO_SECRET_KEY")
	if v, err := secret("MINIO_SECRET_KEY"); err != nil || v != "file-secret" {
		t.Fatal("file secret not loaded")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 16385)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := secret("MINIO_SECRET_KEY"); err == nil {
		t.Fatal("oversized secret accepted")
	}
}
