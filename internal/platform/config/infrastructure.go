package config

import (
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Infrastructure содержит только настройки реализованных сетевых adapters.
// Структуру нельзя целиком логировать: URL и credentials могут содержать секреты.
type Infrastructure struct {
	Postgres Postgres
	RedisURL string
	Kafka    Kafka
	Storage  Storage
	Timeout  time.Duration
}

// Postgres разделяет connection string и ограничения пула. Migration URL загружается
// отдельно: runtime никогда не получает credentials с правами изменения схемы.
type Postgres struct {
	URL      string
	MaxConns int32
}

// Kafka описывает bootstrap brokers и SCRAM-SHA-256 поверх TLS в production.
// PLAINTEXT предназначен только для изолированного development/test окружения.
type Kafka struct {
	Brokers          []string
	SecurityProtocol string
	Username         string
	Password         string
}

// Storage задаёт S3 endpoint и один private bucket. Ключи не включаются в URL.
// Region указан явно, чтобы проверки не требовали автоматического определения региона.
type Storage struct {
	Endpoint  string
	Secure    bool
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
}

// secret поддерживает VALUE либо VALUE_FILE, но никогда оба. Из файла читается
// ограниченный объём, одна завершающая CR/LF удаляется для Docker secrets.
// Ошибка называет только настройку, не путь файла и не содержимое.
func secret(name string) (string, error) {
	v, direct := os.LookupEnv(name)
	path, file := os.LookupEnv(name + "_FILE")
	if direct && file {
		return "", errors.New(name + " and " + name + "_FILE are mutually exclusive")
	}
	if file {
		f, err := os.Open(path)
		if err != nil {
			return "", errors.New(name + "_FILE cannot be read")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 16385))
		if err != nil || len(data) > 16384 {
			return "", errors.New(name + "_FILE is unreadable or too large")
		}
		v = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	}
	if strings.TrimSpace(v) == "" || len(v) > 16384 {
		return "", errors.New(name + " is required and must be at most 16384 bytes")
	}
	return v, nil
}

// setting применяет default лишь при отсутствии переменной; пустая строка остаётся
// невалидным явным значением и отклоняется соответствующей проверкой.
func setting(name, fallback string) string {
	if v, ok := os.LookupEnv(name); ok {
		return strings.TrimSpace(v)
	}
	return fallback
}

// LoadPostgres валидирует URL без подключения, требуя явные host/database/credentials.
// Допускается лишь явная TLS policy; production требует проверку hostname/CA.
func LoadPostgres(environment string, migration bool) (Postgres, error) {
	name := "POSTGRES_URL"
	if migration {
		name = "MIGRATION_POSTGRES_URL"
	}
	raw, err := secret(name)
	if err != nil {
		return Postgres{}, err
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" || u.Hostname() == "" || u.User == nil || u.User.Username() == "" || u.Path == "" || u.Path == "/" || u.Fragment != "" {
		return Postgres{}, errors.New(name + " must be an explicit PostgreSQL URL with user, host and database")
	}
	if password, ok := u.User.Password(); !ok || password == "" {
		return Postgres{}, errors.New(name + " requires an explicit password")
	}
	if !validPort(u.Port()) {
		return Postgres{}, errors.New(name + " has an invalid port")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Postgres{}, errors.New(name + " has invalid parameters")
	}
	for key, values := range q {
		if (key != "sslmode" && key != "sslrootcert") || len(values) != 1 {
			return Postgres{}, errors.New(name + " only accepts sslmode and sslrootcert parameters")
		}
	}
	mode := q.Get("sslmode")
	if mode != "disable" && mode != "verify-full" || environment == "production" && mode != "verify-full" {
		return Postgres{}, errors.New(name + " requires explicit sslmode=disable (dev/test only) or verify-full")
	}
	max, err := strconv.ParseInt(setting("POSTGRES_MAX_CONNS", "10"), 10, 32)
	if err != nil || max < 1 || max > 100 {
		return Postgres{}, errors.New("POSTGRES_MAX_CONNS must be between 1 and 100")
	}
	return Postgres{URL: raw, MaxConns: int32(max)}, nil
}

// LoadStorage используется runtime и явной minio-init командой; credentials
// выбираются оператором для конкретного процесса и не записываются в bucket policy.
func LoadStorage(environment string) (Storage, error) {
	u, err := url.Parse(setting("MINIO_ENDPOINT", "http://127.0.0.1:9000"))
	if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" || u.Scheme != "http" && u.Scheme != "https" || environment == "production" && u.Scheme != "https" {
		return Storage{}, errors.New("MINIO_ENDPOINT must be an HTTP(S) origin; HTTPS is required in production")
	}
	if !validPort(u.Port()) {
		return Storage{}, errors.New("MINIO_ENDPOINT has an invalid port")
	}
	bucket := setting("MINIO_BUCKET", "chat-attachments")
	if len(bucket) < 3 || len(bucket) > 63 || bucket[0] == '-' || bucket[len(bucket)-1] == '-' || net.ParseIP(bucket) != nil || strings.ContainsAny(bucket, "._") || strings.IndexFunc(bucket, func(r rune) bool { return r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') }) != -1 {
		return Storage{}, errors.New("MINIO_BUCKET must contain 3-63 lowercase letters, digits or interior hyphens")
	}
	access, err := secret("MINIO_ACCESS_KEY")
	if err != nil {
		return Storage{}, err
	}
	key, err := secret("MINIO_SECRET_KEY")
	if err != nil {
		return Storage{}, err
	}
	region := setting("MINIO_REGION", "us-east-1")
	if region == "" {
		return Storage{}, errors.New("MINIO_REGION must not be empty")
	}
	return Storage{Endpoint: u.Host, Secure: u.Scheme == "https", Region: region, Bucket: bucket, AccessKey: access, SecretKey: key}, nil
}

// LoadInfrastructure выполняется до bind. Каждая ошибка безопасна для startup log;
// параметры драйверов, полученные из недоверенных строк, логировать запрещено.
func LoadInfrastructure(environment string) (Infrastructure, error) {
	var cfg Infrastructure
	var err error
	cfg.Postgres, err = LoadPostgres(environment, false)
	if err != nil {
		return Infrastructure{}, err
	}
	cfg.RedisURL, err = secret("REDIS_URL")
	if err != nil {
		return Infrastructure{}, err
	}
	u, err := url.Parse(cfg.RedisURL)
	if err != nil || u.Hostname() == "" || u.Scheme != "redis" && u.Scheme != "rediss" || u.RawQuery != "" || u.Fragment != "" || environment == "production" && u.Scheme != "rediss" {
		return Infrastructure{}, errors.New("REDIS_URL must be redis(s)://host/db without query; production requires rediss")
	}
	if environment == "production" {
		if u.User == nil {
			return Infrastructure{}, errors.New("REDIS_URL requires credentials in production")
		}
		if p, _ := u.User.Password(); p == "" {
			return Infrastructure{}, errors.New("REDIS_URL requires password in production")
		}
	}
	if !validPort(u.Port()) {
		return Infrastructure{}, errors.New("REDIS_URL has an invalid port")
	}
	if u.Path != "" && u.Path != "/" {
		db, err := strconv.Atoi(strings.TrimPrefix(u.Path, "/"))
		if err != nil || db < 0 {
			return Infrastructure{}, errors.New("REDIS_URL database must be a nonnegative integer")
		}
	}
	cfg.Timeout, err = time.ParseDuration(setting("INFRA_TIMEOUT", "5s"))
	if err != nil || cfg.Timeout < 100*time.Millisecond || cfg.Timeout > 30*time.Second {
		return Infrastructure{}, errors.New("INFRA_TIMEOUT must be between 100ms and 30s")
	}
	cfg.Kafka.Brokers = strings.Split(setting("KAFKA_BROKERS", "127.0.0.1:9092"), ",")
	for _, addr := range cfg.Kafka.Brokers {
		host, port, err := net.SplitHostPort(addr)
		n, e := strconv.Atoi(port)
		if err != nil || host == "" || e != nil || n < 1 || n > 65535 {
			return Infrastructure{}, errors.New("KAFKA_BROKERS must be comma-separated host:port addresses")
		}
	}
	cfg.Kafka.SecurityProtocol = setting("KAFKA_SECURITY_PROTOCOL", "PLAINTEXT")
	switch cfg.Kafka.SecurityProtocol {
	case "PLAINTEXT":
		if environment == "production" {
			return Infrastructure{}, errors.New("KAFKA_SECURITY_PROTOCOL must be SASL_SSL in production")
		}
	case "SASL_SSL":
		cfg.Kafka.Username, err = secret("KAFKA_USERNAME")
		if err != nil {
			return Infrastructure{}, err
		}
		cfg.Kafka.Password, err = secret("KAFKA_PASSWORD")
		if err != nil {
			return Infrastructure{}, err
		}
	default:
		return Infrastructure{}, errors.New("KAFKA_SECURITY_PROTOCOL must be PLAINTEXT or SASL_SSL")
	}
	cfg.Storage, err = LoadStorage(environment)
	if err != nil {
		return Infrastructure{}, err
	}
	return cfg, nil
}

// validPort допускает отсутствие порта для default scheme port, иначе только цифры
// в TCP диапазоне. URL parser сам по себе не проверяет верхнюю границу 65535.
func validPort(port string) bool {
	if port == "" {
		return true
	}
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535 && strings.IndexFunc(port, func(r rune) bool { return r < '0' || r > '9' }) == -1
}
