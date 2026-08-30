// Package config loads the settings implemented by the current application.
// It does not read .env files or silently accept invalid values as defaults.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Service выбирает роль процесса и её defaults, но не уровень пользовательских прав.
type Service string

const (
	// API обслуживает внешний HTTP transport; бизнес-маршруты подключаются позднее.
	API Service = "chat-api"
	// Worker имеет собственный порт probes, чтобы обе роли запускались на одном хосте.
	Worker Service = "chat-worker"
)

// Config — проверенный снимок environment при запуске, без горячей перезагрузки.
// Настройки будущих зависимостей добавляются вместе с реальными adapters.
type Config struct {
	Service         Service
	Environment     string
	ShutdownTimeout time.Duration
	Log             Log
	HTTP            HTTP
}

// Log определяет минимальный уровень и формат вывода; production допускает только JSON.
type Log struct {
	Level  slog.Level
	Format string
}

// HTTP ограничивает время и объём работы transport. MaxBodyBytes применяется middleware,
// остальные лимиты передаются net/http. Address содержит literal IP и явный порт.
type HTTP struct {
	Address           string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
	MaxBodyBytes      int64
}

// Load читает environment один раз и возвращает либо валидный Config, либо нулевое
// значение с перечнем нарушений. Ошибки безопасны для логов: входные значения скрыты.
func Load(service Service) (Config, error) {
	return load(service, os.LookupEnv)
}

// load отделяет разбор от ОС: тесты подставляют lookup без изменения environment.
// Все нарушения собираются за один проход, чтобы исправить конфигурацию за один запуск.
func load(service Service, lookup func(string) (string, bool)) (Config, error) {
	if service != API && service != Worker {
		return Config{}, errors.New("unsupported service")
	}
	var problems []error
	// Отсутствующее значение допускает default, явно пустое является ошибкой.
	// Возврат fallback после ошибки нужен лишь для продолжения диагностики.
	value := func(name, fallback string) string {
		v, exists := lookup(name)
		if !exists {
			return fallback
		}
		if strings.TrimSpace(v) == "" {
			problems = append(problems, fmt.Errorf("%s must not be empty", name))
			return fallback
		}
		return strings.TrimSpace(v)
	}
	// Общая граница не позволяет отключить timeouts нулём или задать бесконечное ожидание.
	duration := func(name, fallback string) time.Duration {
		d, err := time.ParseDuration(value(name, fallback))
		if err != nil || d < time.Millisecond || d > 10*time.Minute {
			problems = append(problems, fmt.Errorf("%s must be a duration between 1ms and 10m", name))
			return 0
		}
		return d
	}
	// Диапазон задаёт вызывающий код; ошибки парсера с исходной строкой не публикуются.
	integer := func(name, fallback string, min, max int64) int64 {
		n, err := strconv.ParseInt(value(name, fallback), 10, 64)
		if err != nil || n < min || n > max {
			problems = append(problems, fmt.Errorf("%s must be an integer between %d and %d", name, min, max))
			return 0
		}
		return n
	}

	env := value("APP_ENV", "")
	switch env {
	case "development", "test", "production":
	default:
		problems = append(problems, errors.New("APP_ENV is required and must be development, test or production"))
	}
	level := slog.LevelInfo
	switch value("LOG_LEVEL", "info") {
	case "debug":
		level = slog.LevelDebug
	case "info":
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		problems = append(problems, errors.New("LOG_LEVEL must be debug, info, warn or error"))
	}
	format := value("LOG_FORMAT", "json")
	if format != "json" && format != "text" {
		problems = append(problems, errors.New("LOG_FORMAT must be json or text"))
	}
	if env == "production" && format != "json" {
		problems = append(problems, errors.New("LOG_FORMAT must be json in production"))
	}
	address := "127.0.0.1:8080"
	if service == Worker {
		address = "127.0.0.1:8081"
	}
	address = value("HTTP_ADDR", address)
	host, port, err := net.SplitHostPort(address)
	// Literal IP исключает зависимость startup validation от DNS. Проверка цифр
	// дополнительно отклоняет знак '+', который Atoi допускает для целых чисел.
	if err != nil || net.ParseIP(host) == nil {
		problems = append(problems, errors.New("HTTP_ADDR must contain a literal IP address and numeric port"))
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		problems = append(problems, errors.New("HTTP_ADDR port must be between 1 and 65535"))
	} else if strings.IndexFunc(port, func(r rune) bool { return r < '0' || r > '9' }) != -1 {
		problems = append(problems, errors.New("HTTP_ADDR port must contain decimal digits only"))
	}
	// Bootstrap has neither TLS nor authenticated business routes yet. Do not
	// expose it off-host in production while those dependencies are missing.
	if env == "production" && net.ParseIP(host) != nil && !net.ParseIP(host).IsLoopback() {
		problems = append(problems, errors.New("HTTP_ADDR must be loopback in production until TLS deployment is configured"))
	}
	cfg := Config{
		Service: service, Environment: env,
		ShutdownTimeout: duration("APP_SHUTDOWN_TIMEOUT", "10s"),
		Log:             Log{Level: level, Format: format},
		HTTP: HTTP{
			Address:           address,
			ReadHeaderTimeout: duration("HTTP_READ_HEADER_TIMEOUT", "5s"),
			ReadTimeout:       duration("HTTP_READ_TIMEOUT", "10s"),
			WriteTimeout:      duration("HTTP_WRITE_TIMEOUT", "15s"),
			IdleTimeout:       duration("HTTP_IDLE_TIMEOUT", "60s"),
			MaxHeaderBytes:    int(integer("HTTP_MAX_HEADER_BYTES", "32768", 1024, 1<<20)),
			MaxBodyBytes:      integer("HTTP_MAX_BODY_BYTES", "1048576", 1, 16<<20),
		},
	}
	// Заголовки входят в общий read timeout; отдельное окно не должно быть длиннее.
	if cfg.HTTP.ReadHeaderTimeout > cfg.HTTP.ReadTimeout {
		problems = append(problems, errors.New("HTTP_READ_HEADER_TIMEOUT must not exceed HTTP_READ_TIMEOUT"))
	}
	if len(problems) > 0 {
		// Diagnostics contain field names and constraints, never supplied values.
		return Config{}, fmt.Errorf("invalid configuration: %w", errors.Join(problems...))
	}
	return cfg, nil
}
