package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

// fromMap: Подменяет os.LookupEnv, сохраняя различие между отсутствующим и явно пустым значением.
func fromMap(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
}

// TestDefaultsRequireExplicitEnvironment: Проверяет fail-fast без APP_ENV и безопасные, разные defaults для двух процессов.
func TestDefaultsRequireExplicitEnvironment(t *testing.T) {
	if _, err := load(API, fromMap(nil)); err == nil || !strings.Contains(err.Error(), "APP_ENV") {
		t.Fatal("missing APP_ENV must fail before startup")
	}
	api, err := load(API, fromMap(map[string]string{"APP_ENV": "development"}))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := load(Worker, fromMap(map[string]string{"APP_ENV": "development"}))
	if err != nil {
		t.Fatal(err)
	}
	if api.HTTP.Address != "127.0.0.1:8080" || worker.HTTP.Address != "127.0.0.1:8081" {
		t.Fatalf("default listeners must be distinct and loopback: %q %q", api.HTTP.Address, worker.HTTP.Address)
	}
	if api.ShutdownTimeout <= 0 || api.HTTP.ReadHeaderTimeout <= 0 || api.HTTP.MaxBodyBytes <= 0 {
		t.Fatal("timeouts and body bounds must be enabled by default")
	}
}

// TestInvalidConfigurationDoesNotLeakValues: Проверяет границы настроек и запрет выдачи частичного Config либо секретов в ошибках.
func TestInvalidConfigurationDoesNotLeakValues(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"APP_ENV", "secret-environment-value"},
		{"APP_ENV", ""},
		{"LOG_LEVEL", "secret-log-level"},
		{"LOG_FORMAT", "secret-log-format"},
		{"HTTP_ADDR", "https://user:secret-password@example.invalid:8080"},
		{"HTTP_ADDR", "localhost:8080"},
		{"HTTP_ADDR", "127.0.0.1:0"},
		{"HTTP_ADDR", "127.0.0.1:65536"},
		{"HTTP_ADDR", "127.0.0.1:+8080"},
		{"HTTP_ADDR", "127.0.0.1:secret-port"},
		{"APP_SHUTDOWN_TIMEOUT", "secret-duration"},
		{"APP_SHUTDOWN_TIMEOUT", "0s"},
		{"HTTP_READ_TIMEOUT", "-1s"},
		{"HTTP_READ_HEADER_TIMEOUT", "20s"},
		{"HTTP_WRITE_TIMEOUT", "11m"},
		{"HTTP_IDLE_TIMEOUT", ""},
		{"HTTP_MAX_HEADER_BYTES", "0"},
		{"HTTP_MAX_BODY_BYTES", "-1"},
		{"HTTP_MAX_BODY_BYTES", "16777217"},
		{"HTTP_MAX_BODY_BYTES", "secret-body-limit"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			values := map[string]string{"APP_ENV": "development", tc.key: tc.value}
			cfg, err := load(API, fromMap(values))
			if err == nil {
				t.Fatal("expected invalid configuration")
			}
			if cfg != (Config{}) {
				t.Fatal("failed load returned a usable partial configuration")
			}
			if !strings.Contains(err.Error(), tc.key) || strings.Contains(err.Error(), "secret-") {
				t.Fatalf("diagnostic must identify field without its value: %v", err)
			}
		})
	}
}

// TestProductionBootstrapCannotExposePlainHTTP: Фиксирует временные production-ограничения bootstrap до появления TLS deployment.
func TestProductionBootstrapCannotExposePlainHTTP(t *testing.T) {
	for _, override := range []map[string]string{
		{"APP_ENV": "production", "LOG_FORMAT": "text"},
		{"APP_ENV": "production", "HTTP_ADDR": "0.0.0.0:8080"},
		{"APP_ENV": "production", "HTTP_ADDR": "[::]:8080"},
	} {
		if _, err := load(API, fromMap(override)); err == nil {
			t.Fatal("unsafe production bootstrap settings were accepted")
		}
	}
	if _, err := load(API, fromMap(map[string]string{"APP_ENV": "production", "HTTP_ADDR": "[::1]:8080"})); err != nil {
		t.Fatal(err)
	}
}

// TestValidOverrides: Проверяет применение валидных override и отклонение неизвестной роли процесса.
func TestValidOverrides(t *testing.T) {
	cfg, err := load(Worker, fromMap(map[string]string{
		"APP_ENV": "test", "HTTP_ADDR": "[::1]:9091", "LOG_LEVEL": "warn", "LOG_FORMAT": "text",
		"APP_SHUTDOWN_TIMEOUT": "500ms", "HTTP_MAX_BODY_BYTES": "2048",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Level != slog.LevelWarn || cfg.Log.Format != "text" || cfg.HTTP.Address != "[::1]:9091" ||
		cfg.ShutdownTimeout != 500*time.Millisecond || cfg.HTTP.MaxBodyBytes != 2048 {
		t.Fatalf("valid overrides were not applied: %+v", cfg)
	}
	if _, err := load(Service("unknown"), fromMap(nil)); err == nil {
		t.Fatal("unsupported process role must fail")
	}
}
