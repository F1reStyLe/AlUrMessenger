# Universal Chat Service

Независимый backend чата для нескольких внешних приложений: Go, PostgreSQL, Redis, Kafka,
MinIO, REST, WebSocket и внутренний gRPC. Frontend — простые Admin и Demo на Vue 3/TypeScript/Vite.

Проект начат заново по решению пользователя. Предыдущая реализация удалена; Git history сохранена.
Сейчас подготовлена **архитектурная Phase 0**. Исполняемого backend/frontend, Go module,
Docker Compose и SQL migrations пока нет. Запуск `docker compose up` станет доступен в Foundation;
полный MVP — после поэтапной реализации. Не считать проект готовым к production.

## Документы

| Документ | Содержание |
| --- | --- |
| [Исходное ТЗ](docs/REQUIREMENTS.md) | Полный перечень требований и acceptance criteria |
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

Phase 1 — Foundation: Go skeleton, configuration/logging/lifecycle, PostgreSQL/migrations,
Redis/Kafka/MinIO, core Compose, JWT/Project/User, dev-token/seed, flags, security limits и tests.
После появления каждого компонента README дополняется точными командами, ports и примерами.

Целевой локальный процесс — clone → development configuration/secret initialization →
`docker compose up --build`. Полный набор служб: postgres, redis, kafka, minio, minio-init,
chat-api, chat-worker, admin-web, demo-web. Production secrets и TLS не берутся из dev defaults.
Существующие внешние БД и volumes не удаляются автоматически.

## Правила работы

Работа идёт по фазам, без заглушек для acceptance текущего шага. Результаты, решения и отклонения
записываются после каждого шага в локальный `Agents.md` (исключён из Git). В versioned документации
поддерживаются актуальные contracts и статус фазы. Commit и push — только по прямому требованию.

Проверка Phase 0 — полнота/согласованность документов, локальных ссылок и Git diff.
Сборка и runtime tests нового сервиса пока неприменимы: кода ещё нет.
