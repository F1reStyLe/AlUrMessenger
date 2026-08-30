# Universal Chat Service

Независимый backend чата для нескольких внешних приложений: Go, PostgreSQL, Redis, Kafka,
MinIO, REST, WebSocket и внутренний gRPC. Frontend — простые Admin и Demo на Vue 3/TypeScript/Vite.

Проект начат заново по решению пользователя. Предыдущая реализация удалена; Git history сохранена.
Phase 0 завершена; реализованы **шаги 1.1–1.4 Foundation**: API/worker, конфигурация,
логи, HTTP lifecycle, PostgreSQL/Redis/Kafka/MinIO adapters и отдельные команды миграций/init.
Добавлены development Compose, Project/User, внешний JWT verifier, профили и dev seed/token.
Чата, jobs и frontend пока нет. [Identity API и запуск seed](docs/IDENTITY.md).
Readiness подтверждает готовность инфраструктуры, а не всего Chat API.

## Локальный запуск

Основной путь — [development Compose](docs/DEVELOPMENT.md):

```powershell
$env:APP_ENV = 'development'
go run ./cmd/dev-init
docker compose up -d --build --wait --wait-timeout 180
```

Секреты в `.local/` и данные volumes сохраняются при повторных запусках. Это не production deployment.

Toolchain закреплён на Go 1.27.0. Установленный Go с `GOTOOLCHAIN=auto` загрузит его по go.mod;
при отключённой автоматической загрузке установите нужный toolchain отдельно.
Выбор версии: [официальные релизы Go](https://go.dev/dl/).
Версии клиентов закреплены в go.mod/go.sum. Инфраструктура и credentials теперь обязательны;
одного APP_ENV недостаточно. Подготовка сервисов, секретов и migrations описана в
[инструкции Foundation 1.2](docs/INFRASTRUCTURE.md).

Воспроизводимая проверка с отдельными тестовыми сервисами (PowerShell 7, Docker и C compiler):

```powershell
./scripts/test-infrastructure.ps1
```

Скрипт сам создаёт и удаляет только свой test fixture. Для обычного запуска ниже заранее
подготовьте зависимости, примените `go run ./cmd/migrate up`, выполните `go run ./cmd/minio-init`
с правами оператора и передайте runtime environment отдельно каждому процессу.

PowerShell, из корня репозитория, в первом терминале:

```powershell
$env:APP_ENV = 'development'
go run ./cmd/api
```

Во втором терминале:

```powershell
$env:APP_ENV = 'development'
go run ./cmd/worker
```

В Linux/macOS аналогично: `APP_ENV=development go run ./cmd/api` и
`APP_ENV=development go run ./cmd/worker` в отдельных терминалах.
API по умолчанию слушает `127.0.0.1:8080`, worker probes — `127.0.0.1:8081`.
Worker пока обслуживает только probes/lifecycle; outbox/consumers/jobs ещё не реализованы.

```powershell
curl.exe -i http://127.0.0.1:8080/health/live
curl.exe -i http://127.0.0.1:8080/health/ready
curl.exe -i http://127.0.0.1:8081/health/live
```

Liveness: `200 {"status":"ok"}`. Readiness: `200 {"status":"ready"}` при доступных PostgreSQL/schema,
Redis, Kafka и private MinIO; иначе `503` с кодом `DEPENDENCY_UNAVAILABLE`.
Неизвестные routes → JSON 404, недопустимый метод probe → JSON 405. Поддерживаются GET/HEAD.
Каждый ответ middleware содержит X-Request-ID; безопасный ID также есть в error envelope и structured log.
Ошибки HTTP-парсера/лимита headers до middleware могут не иметь этих headers и JSON envelope.
Текущий [OpenAPI JSON](api/openapi/openapi.json) описывает только реально доступные probes;
Identity API описан в [OpenAPI](api/openapi/openapi.json); Swagger UI добавляется в 1.6.

## Конфигурация и остановка

[.env.example](.env.example) — справочник значений, **.env автоматически не загружается**.
Передавайте environment явно; пример не содержит секретов. APP_ENV обязателен, разрешены
development/test/production. Неверные/пустые заданные значения отклоняются до открытия listener,
а их содержимое не выводится в диагностику.

| Переменная | Default / ограничение |
| --- | --- |
| APP_ENV | Обязательна, без default |
| APP_SHUTDOWN_TIMEOUT | 10s |
| LOG_LEVEL / LOG_FORMAT | info / json; уровни debug/info/warn/error, формат json/text |
| HTTP_ADDR | API 127.0.0.1:8080, worker 127.0.0.1:8081; literal IPv4/IPv6 и port 1–65535 |
| HTTP_READ_HEADER_TIMEOUT | 5s, не больше HTTP_READ_TIMEOUT |
| HTTP_READ_TIMEOUT / HTTP_WRITE_TIMEOUT | 10s / 15s |
| HTTP_IDLE_TIMEOUT | 60s |
| HTTP_MAX_HEADER_BYTES | 32768; 1024–1048576 |
| HTTP_MAX_BODY_BYTES | 1048576; 1–16777216, включая chunked JSON requests |

Указанные application duration settings ограничены 1ms–10m. Production bootstrap допускает
только loopback HTTP и JSON logs: TLS/authentication deployment ещё не реализован.
Настройки POSTGRES/REDIS/KAFKA/MINIO, INFRA_TIMEOUT и secret files описаны в
[инфраструктурной инструкции](docs/INFRASTRUCTURE.md). JWT/ENCRYPTION/CORS/RATE_LIMIT/
UPLOAD/RETENTION/WEBHOOK config вводится вместе с соответствующими компонентами.

Ctrl+C/SIGTERM запускает drain: readiness снимается, новые соединения/запросы не принимаются,
текущие HTTP handlers получают время завершиться. По deadline contexts отменяются, connections
закрываются и процесс завершается с кодом 1. Нормальная остановка — 0; startup/bind failure — 1.
После HTTP drain зависимости закрываются с отдельным budget APP_SHUTDOWN_TIMEOUT;
ошибка cleanup также даёт код 1.
Handler/use case обязан реагировать на context cancellation. WS/gRPC/jobs получат свой drain
при реализации; текущий shutdown не выдаётся за проверку отсутствующих компонентов.

Для запуска именно собранных процессов в Windows:

```powershell
go build -o build/chat-api.exe ./cmd/api
go build -o build/chat-worker.exe ./cmd/worker
```

## Проверки

```text
go test ./...
go test -race ./...
go vet ./...
go build ./...
gofmt -l cmd internal
```

Race detector требует поддерживаемую платформу и C toolchain. Тесты не используют внешние БД:
проверяют config/fail-fast, request IDs, redaction, JSON/size limits и lifecycle на настоящем
loopback listener, включая drain и принудительную остановку по deadline.

Infrastructure suite дополнительно проверяет concurrent/idempotent migrations, запрет DDL runtime,
Redis TTL, Kafka produce/consume, приватность S3, сбой и восстановление каждой зависимости.

## Документы

| Документ | Содержание |
| --- | --- |
| [Исходное ТЗ](docs/REQUIREMENTS.md) | Полный перечень требований и acceptance criteria |
| [Инфраструктура 1.2](docs/INFRASTRUCTURE.md) | Секреты, миграции, readiness, fixture и ограничения MinIO |
| [ARCHITECTURE.md](ARCHITECTURE.md) | Modular monolith, модули, API/worker, потоки и масштабирование |
| [DECISIONS.md](DECISIONS.md) | Принятые решения и компромиссы |
| [DB_SCHEMA.md](DB_SCHEMA.md) | Таблицы, tenant constraints, sequence, outbox и cleanup |
| [API_DESIGN.md](API_DESIGN.md) | REST/gRPC contracts, DTO/errors/scopes, pagination и flags |
| [WEBSOCKET_PROTOCOL.md](WEBSOCKET_PROTOCOL.md) | Handshake, commands/events/ack, recovery и devices |
| [EVENTS.md](EVENTS.md) | Kafka contracts, ordering/dedup, webhooks/HMAC/retries |
| [SECURITY.md](SECURITY.md) | Auth/permissions, encryption/search, files, SSRF и secrets |
| [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) | Фазы 0–10, DoD, трассировка ТЗ и сценарий приёмки |

## Основные гарантии проекта

- Обязательный Project context в service/repository, FK, событиях, Redis и MinIO.
- Внешний Auth проверяет пользователя; Chat проверяет JWT и создаёт минимальный профиль.
- PostgreSQL — источник истины. Message + sequence + changefeed + outbox — одна transaction.
- Повтор client_message_id не создаёт новое сообщение в пределах documented dedup window.
- Message sequence задаёт историю; event sequence восстанавливает также edits/deletes/reactions.
- Сообщения encrypted at rest, MinIO private. Поиск по полным словам использует HMAC index;
  его ограничения/утечки явно описаны в security model. E2EE в MVP не входит.

Это требования к будущей реализации, не подтверждённые свойства работающего приложения.

## Следующий этап

Следующий шаг **1.5**: policies/limits; затем Swagger UI/приёмочные проверки (1.6).
Foundation целиком пока не завершена.

Целевой локальный процесс — clone → development configuration/secret initialization →
`docker compose up --build`. Полный набор служб: postgres, redis, kafka, minio, minio-init,
chat-api, chat-worker, admin-web, demo-web. Production secrets и TLS не берутся из dev defaults.
Существующие внешние БД и volumes не удаляются автоматически.

## Правила работы

Работа идёт по фазам, без заглушек для acceptance текущего шага. Результаты, решения и отклонения
записываются после каждого шага в локальный `Agents.md` (исключён из Git). В versioned документации
поддерживаются актуальные contracts и статус фазы. Commit и push — только по прямому требованию.

История решений и локальный журнал отличают выполненные шаги от будущих возможностей MVP.
