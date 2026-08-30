# Foundation 1.2 — инфраструктура

Реализованы pgx pool, goose migrations, Redis client, Kafka client и private S3/MinIO adapter.
API и worker подключают их при старте и освобождают после HTTP drain. Business handlers,
outbox/consumers/jobs и JWT ещё отсутствуют. Development Compose добавлен на шаге 1.3:
[инструкция запуска](DEVELOPMENT.md).

## Подготовка и запуск

Runtime требует предварительно подготовленные PostgreSQL, Redis, Kafka KRaft и MinIO bucket.
Для воспроизводимой проверки с нуля используйте **PowerShell 7**, Docker Linux containers,
Go toolchain из go.mod и C compiler для race detector:

```powershell
./scripts/test-infrastructure.ps1
```

Скрипт создаёт отдельный Compose project со случайным суффиксом и свободными loopback ports,
генерирует временные пароли в ignored `build/`, запускает integration/race tests, команды
migrate/minio-init и оба Linux-бинарника с проверкой SIGTERM. В `finally` удаляет только свой
project, его volumes и временные файлы. Обычный `go test ./...` не обращается к инфраструктуре;
integration tests включаются явным build tag и защитным environment flag.

Проверка `govulncheck v1.7.0` не обнаружила достижимых уязвимостей или уязвимостей в
импортируемых пакетах. На уровне зависимого модуля x/crypto отмечен
[GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932) для неиспользуемого openpgp;
этот пакет сервис не импортирует. Проверка Go-кода не является аудитом server images.

`test/integration/compose.yaml` — одноразовый test fixture, не production deployment.
PostgreSQL, Redis AOF и MinIO используют отдельные volumes внутри уникального project.
Kafka работает одним KRaft broker/controller без ZooKeeper, с отключённым auto-create topics.
Порты не публикуются на внешние интерфейсы. Тестовый Kafka PLAINTEXT не подходит для production.
Не запускайте integration tests против существующей БД: они меняют migration version,
создают topic/object и проверяют отказы, временно останавливая свои контейнеры.

Для собственных уже подготовленных сервисов экспортируйте environment согласно
[.env.example](../.env.example); `.env` автоматически не читается. Затем:

```text
go run ./cmd/migrate up
go run ./cmd/migrate status
go run ./cmd/minio-init
go run ./cmd/api
go run ./cmd/worker
```

API/worker запускаются в отдельных терминалах. Для `migrate` нужны APP_ENV и
MIGRATION_POSTGRES_URL, для `minio-init` — APP_ENV и MINIO настройки. Инициализатору bucket
передаются credentials оператора; runtime credentials не должны иметь CreateBucket/SetBucketPolicy.
Повторный init существующего private bucket возможен с GetBucketPolicy/ListBucket правами.
Runtime не получает MIGRATION_POSTGRES_URL и не вызывает миграции при запуске.

## Environment и секреты

| Настройка | Условие / default |
| --- | --- |
| APP_ENV | development, test или production; обязательно |
| POSTGRES_URL | Обязательный postgres(ql) URL: host, database, user и password |
| MIGRATION_POSTGRES_URL | Отдельный URL с DDL правами; только migrate |
| POSTGRES_MAX_CONNS | 10; диапазон 1–100 |
| REDIS_URL | Обязательный redis(s) URL, без query parameters, database ≥ 0 |
| INFRA_TIMEOUT | 5s; 100ms–30s, startup check и сетевые timeouts |
| KAFKA_BROKERS | 127.0.0.1:9092; comma-separated host:port |
| KAFKA_SECURITY_PROTOCOL | PLAINTEXT для dev/test или SASL_SSL |
| KAFKA_USERNAME / KAFKA_PASSWORD | Обязательны при SASL_SSL; SCRAM-SHA-256 |
| MINIO_ENDPOINT | http://127.0.0.1:9000; origin без credentials/path/query |
| MINIO_ACCESS_KEY / MINIO_SECRET_KEY | Обязательны, без встроенных defaults |
| MINIO_REGION | us-east-1; непустая строка |
| MINIO_BUCKET | chat-attachments; 3–63 lowercase letters/digits/hyphens, без dots |

Для URL и credentials поддерживаются NAME либо NAME_FILE, не оба одновременно.
Файлы до 16 KiB; завершающий CR/LF удаляется. Ошибки чтения не раскрывают путь/содержимое.
Пароли внутри URL кодируются percent-encoding. Secret files, environment и полные config
structs нельзя публиковать или писать в логи. Startup сообщает только имя настройки или код.

PostgreSQL URL допускает query только `sslmode` и `sslrootcert`, без duplicate параметров.
`sslmode` обязателен: `disable` разрешён только вне production, `verify-full` проверяет CA/hostname.
Production Redis требует `rediss` и password; Kafka — SASL_SSL; MinIO — HTTPS.
Kafka/MinIO используют системные trust roots, без insecure skip-verify switches.
Production TLS/ACL deployment не подтверждён локальными plaintext integration tests.
HTTP production listener пока ограничен loopback до реализации TLS ingress на следующих шагах.

## PostgreSQL и миграции

Версия 1 создаёт схему `chat`, выдаёт runtime только USAGE/DML default privileges и SELECT
на `public.goose_db_version`. Domain tables ещё нет: Project/User появятся в 1.4.
SQL встроен в бинарник из [migrations](../migrations/00001_foundation.sql).

Перед миграцией оператор создаёт `alur_runtime` и отдельный migration account без SUPERUSER,
CREATEDB/CREATEROLE. Runtime не владеет database/schema и не имеет CREATE в public/chat;
migration account получает CREATE на database/public. Смотрите конкретный fixture provisioning
в [postgres-init.sh](../test/integration/postgres-init.sh). Для существующих БД права проверяет оператор;
скрипт рассчитан только на новую тестовую БД и не является универсальным upgrade script.

`migrate up` сериализуется PostgreSQL session advisory lock и применяет SQL транзакционно;
повторный вызов не создаёт повторных объектов. Используется один механизм — goose Provider,
без глобального registry и без SQL в init(). CLI не предоставляет разрушительные down/reset.
Общий deadline команды — две минуты, ожидание advisory lock ограничено. Ошибки goose наружу
выдаются безопасным кодом, SQL/DSN не логируются.

Runtime проверяет применённые версии и доступ к schema read-only запросом; отсутствующая,
старая или будущая версия не позволяет стартовать. Миграционная история должна иметь
последовательные положительные версии. Новые SQL сопровождаются обновлением embedded Version.

## Readiness, приватность и остановка

GET/HEAD `/health/live` возвращает 200 без сетевых проверок. `/health/ready` проверяет
PostgreSQL/schema, Redis PING, Kafka metadata и наличие private bucket параллельно под
общим deadline 1s. При успехе — `200 {"status":"ready"}`, при сбое — JSON 503 без адресов,
credentials или подробностей инфраструктуры. HEAD не содержит body.

На этом шаге обе роли имеют консервативную infrastructure readiness: **все четыре** зависимости
обязательны. Это ещё не готовность защищённого Chat API. При подключении business handlers
архитектурная политика capability/degraded readiness из ARCHITECTURE должна разделить
запись в PostgreSQL/outbox и временную недоступность Kafka/MinIO; сейчас она не имитируется.

Kafka Ping подтверждает связь хотя бы с одним broker; он не доказывает права конкретного
topic/consumer group. Runtime не создаёт topics и не запускает фиктивные jobs. Producer
настроен с all ISR ack, стандартной idempotence franz-go и ограничением буфера/срока доставки;
доменные outbox ordering/retries вводятся позже.

MinIO Check требует существующий bucket и **пустую bucket policy**; непустая policy
отклоняется даже при внешне безопасном содержимом. Runtime права задаются IAM policy
для одного bucket: [пример](../test/integration/minio-policy.json). Проверка не меняет policy
и не доказывает отсутствие сетевых proxy/IAM ошибок вне контроля сервиса. Integration test
отдельно подтверждает отказ anonymous GET реального объекта и запрет SetBucketPolicy runtime.

При старте недоступная инфраструктура завершает процесс с кодом 1 до обслуживания HTTP;
после успешного старта сбой снимает readiness, но не останавливает процесс. Повторные проверки
после восстановления сервисов возвращают 200. SIGINT/SIGTERM сначала завершает HTTP drain,
затем закрывает MinIO transport, Kafka/Redis clients и PostgreSQL pool. HTTP drain и cleanup
имеют отдельные бюджеты APP_SHUTDOWN_TIMEOUT; превышение любого даёт exit 1. Будущие
jobs/producer flush обязаны завершиться до закрытия clients и пока не реализованы.

## Ограничение MinIO Community

[Официальный репозиторий MinIO](https://github.com/minio/minio) архивирован и помечен как
неподдерживаемый. Старый pinned server в fixture используется только для compatibility tests,
не рекомендуется как production server. S3 adapter отделён от deployment, но выбор поддерживаемого
production storage требует отдельного решения; замена технологии из ТЗ здесь не выполнялась.
