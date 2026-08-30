# План реализации с нуля

Исходное [ТЗ](docs/REQUIREMENTS.md) обязательно; архитектурные документы описывают целевой MVP,
а не уже работающие функции. Работать строго фазами; внутри фазы — небольшими законченными шагами.
Commit/push — только по прямому требованию пользователя. Локальный журнал шагов ведётся в
игнорируемом `Agents.md`; результаты фазы и инструкции запуска отражаются в versioned документации.

## Порядок работы и Definition of Done

Перед шагом: прочитать актуальный план/код, определить минимальный scope и затрагиваемые файлы.
После: formatter, целевые tests, исправления, документация, запись результата/решений/отклонений.
Статусы не подменяют проверки; TODO для acceptance текущей фазы запрещён.

Для реализации DoD: код собирается, инфраструктура этой фазы запускается, migrations применяются,
базовые и критичные негативные tests проходят, API документирован, нет известных критических
race/data-loss проблем. Следующую большую фазу не начинать при нарушении этих условий.
Для Phase 0 DoD документальный: полнота, согласованность, проверка ссылок и трассировка ТЗ.

Проверки Go из корня будущего module: `gofmt`, `go test ./...`, `go vet ./...`,
`go test -race ./...` на поддерживаемой среде. Интеграционные тесты — в изолированных
PostgreSQL/Redis/Kafka/MinIO с временными данными, никогда на пользовательской БД.

## Phase 0 — Architecture

| Шаг | Результат | Приёмка |
| --- | --- | --- |
| 0.1 | Удалены прежние исходники и generated artifacts; сохранены Git/history/правила и исходное ТЗ | Нет зависимости от старой реализации; данные внешних окружений не затронуты |
| 0.2 | ARCHITECTURE.md | Границы, зависимости, scaling, API/worker и source of truth |
| 0.3 | DECISIONS.md | Спорные решения, ограничения поиска/delivery/retention и причины видны |
| 0.4 | DB_SCHEMA.md | Tenant FK/unique, sequence, outbox, files/recovery/cleanup |
| 0.5 | API_DESIGN.md | Auth/scopes, routes/DTO/errors, cursor, internal gRPC |
| 0.6 | WEBSOCKET_PROTOCOL.md | Auth, ack, events, recovery, multi-device и permissions |
| 0.7 | EVENTS.md | Kafka ordering, dedup, webhook/HMAC/retries, будущие notifications |
| 0.8 | SECURITY.md | Trust boundaries, encryption/search, secrets, files, SSRF, negative tests |
| 0.9–0.10 | Этот план и README; сводная проверка | Каждому требованию назначена фаза; нет ложного обещания работающего runtime |

## Phase 1 — Foundation

Шаги 1.1–1.2 реализованы: bootstrap/API/worker, config/logging/HTTP lifecycle, infrastructure
adapters, отдельные migrate/minio-init commands и unit/integration tests.
Выполненные runtime проверки фиксируются в README, docs/FOUNDATION и локальном журнале.
Шаг 1.3 добавляет development Compose/Dockerfiles/dev-init с постоянными volumes и отдельными jobs.
Шаг 1.4 выполнен: Project/User/JWT/профили/provisioning/seed, unit + repository security tests.
Шаг 1.5 выполнен: Project flags/settings, admin permissions, Redis limits, CORS и append-only audit.
После Foundation уточнён контракт Auth (D32): live JWT/session check через Auth, локальные
роли Chat, operator user-role, migration 4. JWT role claims больше не назначают полномочия.
Шаг 1.6 выполнен: embedded Swagger UI, OpenAPI validation, unit/repository/HTTP acceptance.
Foundation завершён; следующий шаг — Phase 2. Реализованные endpoints описаны в OpenAPI; test fixture Compose
находится в test/integration. Readiness возвращает 200 только при доступных четырёх зависимостях
и ожидаемой schema. Текущие probes описаны в `api/openapi/openapi.json`.

Затрагиваются: go.mod, cmd/api/worker/migrate/project/dev-token/seed, internal/platform,
auth/projects/users/feature_flags/audit, migrations, deploy, compose.yaml, .env.example, OpenAPI, README.

1.1 Создать один Go module, composition roots, typed config и startup validation, slog/request_id,
HTTP error contract, health/readiness и bounded graceful shutdown; базовые unit tests.
1.2 Подключить pgx/goose, Redis, Kafka KRaft, private MinIO. Миграции отдельной командой;
локальные PostgreSQL/Redis volumes, secrets и minio-init. Проверить adapters и readiness failures.
1.3 Собрать Compose core с API/worker, dev-only initialization и migration job; версии pinned,
Dockerfiles multi-stage/non-root, .dockerignore, env/secret-file конфигурация без утечек.
1.4 Реализовать Project CLI provisioning, JWT verifier local public key/JWKS, User auto-provision/profile,
typed Actor и tenant-scoped access. Dev-token/seed проверяют environment и не работают в production.
1.5 Базовые global roles, project flags/settings, Redis per-IP/per-user limiter, origins/CORS,
append-only audit primitive для будущих use cases.
1.6 OpenAPI/Swagger UI, unit/repository tests и воспроизводимые команды запуска/seed в README.

Приёмка: core Compose запускается, migration/init повторяемы, неверная config fail fast, JWT
не даёт impersonation/cross-project access, concurrent auto-provision не создаёт дубликатов,
shutdown завершает workers/соединения. Минимальный seed — Project/admin/users; связанные
conversations/bot добавляются по готовности фаз 2/8. UI-контейнеры появятся в Phase 9, без fake stubs.

## Phase 2 — Conversations

Затрагиваются: conversations/channels, migrations, OpenAPI, repository/integration tests.

2.1 DIRECT/GROUP/CHANNEL и canonical DIRECT pair; REST creation/list/get/patch.
2.2 Membership и роли, creator moderator, защита последнего moderator, leave/kick/ban policies.
2.3 Scoped permissions во всех services/repositories, audit role/moderator actions, dev conversations.
2.4 Тесты concurrent DIRECT creation, tenant FK, creator membership, private CHANNEL write policy,
запрета третьего DIRECT участника и доступа после leave.

Приёмка: доступные conversations создаются/читаются; чужие Project/membership недоступны;
moderator не получает project admin; все ограничения задокументированы и проверены.

## Phase 3 — Messages

Затрагиваются: messages/search, encryption/Kafka adapters, worker/outbox, migrations, API/events.

3.1 TEXT/SYSTEM DTO/persistence, UUID, AES-GCM provider/key versions/AAD, history/cursor.
3.2 Transaction send: idempotency fingerprint, concurrent sequence, changefeed и outbox;
единственное успешное сохранение для повторов, понятный конфликт другого payload.
3.3 Outbox per-aggregate ordering, Kafka acknowledgments, retry/backoff и consumer dedup primitive.
3.4 Search по HMAC words, PostgreSQL indexes, authorization, versioned search keys.
3.5 Unit/repository/concurrency tests: rollback, commit uncertainty/retry, duplicate IDs,
sequence uniqueness, encryption tamper, search privacy и tenant isolation.

Приёмка: принятая отправка сохраняется вместе с outbox; Kafka outage не теряет message;
после восстановления event публикуется, duplicate event_id не удваивает local side effect.
History детерминирована; no plaintext content в DB/search/outbox/logs.

## Phase 4 — WebSocket

Затрагиваются: websocket/presence, Redis routing, changefeed queries, WS protocol/tests.

4.1 Auth handshake, connection/session/hub, limits/deadlines и permissions; никаких JWT query params.
4.2 Message commands через тот же use case, SENT ack, explicit delivered/read checkpoints.
4.3 Durable replay/snapshot, непрерывный event cursor, buffering live hints, periodic catch-up,
несколько API instances и devices; slow consumer close/recovery.
4.4 Redis presence/connections TTL, heartbeat, last_seen batch flush, typing ephemeral TTL;
backend flags/read-only bans и session expiry/revocation.
4.5 WS/integration/race tests, включая два API instances, потерю Redis hints, reconnect после
отключения, expired cursor, outbox duplicates, membership revoke, shutdown и backpressure.

Приёмка: A/B обмениваются сообщениями/read; offline B получает пропуски по sequence без дублей,
открытая session догоняет пропущенный hint, устройства синхронизированы, приватность сохраняется.

## Phase 5 — Message features

Затрагиваются: messages/reactions/pins, migrations, REST/WS/events, replay tests.

5.1 Reply reference, edit с optimistic version и soft delete без выдачи прежнего content.
5.2 Reactions unique(user,message,reaction), pins как отдельные rows, moderator permissions.
5.3 Forward independent snapshot в пределах Project, проверка source/target, удаление источника.
5.4 Backend flags для каждой функции и полноценные changefeed events/current-state hydration.

Приёмка: все операции работают через API/WS без обхода policies; replay edits/deletes/reactions
не воскрешает content и не откатывает version; search index согласован с edit/delete.

## Phase 6 — Attachments

Затрагиваются: attachments/storage objects, MinIO, cleanup job, messages, API/docs/tests.

6.1 Backend streaming image upload, actual MIME/extension/decode/size/pixel limits, project override.
6.2 Private storage, random namespaced keys, logical attachment/message relations, secure short URL.
6.3 IMAGE send/forward, object reference lifecycle, sweeper для незавершённых uploads.
6.4 Тесты invalid files, чужих upload/attachments, bounds/flags, source deletion после forward,
storage outage и idempotent cleanup.

Приёмка: изображение доступно только разрешённому reader; непроверенное/слишком большое не ready;
MinIO private; потеря worker/повтор cleanup не уничтожает object с живыми references.

## Phase 7 — Moderation

Затрагиваются: moderation/reports/audit, policies, API/events/tests.

7.1 Project blacklist add/delete/enable/disable, normalized full-word reject; проверять все
пути content write, включая edit/forward/bot/internal.
7.2 Reports на user/message, review workflow и безопасный DTO, encrypted description.
7.3 Project ban/unban и conversation ban, read-only rules, moderator actions и race ban/send.
7.4 Полное audit coverage административных мутаций, permissions/cache invalidation.

Приёмка: bans/flags/blacklist действуют в application service независимо от transport;
модератор не меняет чужой канал/Project, audit атомарен и не редактируется обычным API.

## Phase 8 — Integration platform

Затрагиваются: api_keys/bots/webhooks/integrations, gRPC/proto, worker consumers, docs/AsyncAPI.

8.1 API keys hashing/create/revoke/rotate/scopes/per-key limits; audit и redaction.
8.2 Webhook subscriptions, HTTPS/HMAC, SSRF-safe HTTP client, durable deliveries/attempts/retries,
pause/disable/expiry/manual retry и отдельная webhook документация.
8.3 Защищённый minimal gRPC, Project allowlist, service capabilities, общие use cases.
8.4 Bot identity/credentials/conversation allowlist и Echo Bot без loop/duplicate responses.
8.5 Kafka/AsyncAPI contract tests, webhook signature/SSRF/retry tests, consumer failure replay,
key revocation/scopes, bot permissions и gRPC authorization.

Приёмка: webhook получает подписанный event и успешно повторяется после сбоя; duplicate event
не удваивает local effects/ответ бота; credentials не позволяют impersonation.

## Phase 9 — Admin + Demo

Затрагиваются: web/admin, web/demo, compose/deploy, seed, README, frontend/integration tests.

9.1 Admin: Dashboard, Users, Conversations, Channels, Reports, Blacklist, API Keys, Webhooks,
Feature Flags, Audit Log, Project Settings. Никаких server permissions в виде только скрытых кнопок.
9.2 Demo: dev JWT, создание/списки/история/send, image/reply/reaction/edit/delete/forward/pin,
typing/presence/read, reconnect/multi-device; реальные API, без fake delivery setTimeout.
9.3 Admin/demo containers, полный seed Project/admin/users/moderator/Echo Bot/conversations.
9.4 Build/typecheck, meaningful tests и полный ручной/browser integration сценарий.

Приёмка: полное development окружение запускается из корня, UI показывает реальное состояние,
Swagger доступен. UI простой, тема/white label/component library не входят в scope.

## Phase 10 — Hardening

Затрагиваются: retention/cleanup workers, fault/security tests, docs/runbooks, configuration limits.

10.1 Retention default 365 дней, scoped settings, safe physical purge и storage cleanup;
changefeed cursor expiry, message/consumer dedup windows, сохранность forward references.
10.2 Security review: auth/tenancy/roles/secrets/files/SSRF/rate limits/encryption/log redaction.
10.3 Полные unit/integration/repository/WS/race проверки и crash/retry/failure scenarios.
10.4 Reproducible clean Compose startup, backup/restore drill, deployment caveats и обновлённый README.
10.5 Полная MVP приёмка по сценарию ниже; известные критические дефекты закрыты.

## Трассировка ТЗ

| Разделы исходного ТЗ | Где проектируется / реализуется |
| --- | --- |
| 1–4, 58–61, 73–76: цели/стек/модули/Project/простота/scaling | ARCHITECTURE, DECISIONS; 0–1 |
| 5–8: JWT, User, роли, conversations | API_DESIGN, SECURITY, DB_SCHEMA; 1–2 |
| 9–12: messages/order/idempotency/outbox | DB_SCHEMA, EVENTS; 3 |
| 13–19: WS/auth/multi-device/recovery/status/typing/presence | WEBSOCKET_PROTOCOL; 4 |
| 20–25: reactions/reply/forward/edit/delete/pin | API_DESIGN, DB_SCHEMA; 5 |
| 26–28: images/limits/MinIO | SECURITY, DB_SCHEMA; 1 инфраструктура, 6 функция |
| 29–30: search/encryption | DECISIONS, SECURITY; 3 |
| 31: retention | DECISIONS, DB_SCHEMA; 10; expires_at/filtering с 3 |
| 32–34, 44: blacklist/reports/bans/audit | SECURITY, API_DESIGN; 7; audit primitive с 1/2 |
| 35–41: bots/webhooks/Kafka/versioning/future notifications/API keys | EVENTS, API_DESIGN; 3 инфраструктура, 8 интеграции |
| 42–43: flags/security settings/rate limits | API_DESIGN, SECURITY; 1 и каждая зависимая функция |
| 45–46: Admin/Demo | API_DESIGN; 9 |
| 47–51: REST/pagination/errors/database/constraints | API_DESIGN, DB_SCHEMA; 1–3 и дальнейшие модули |
| 52–57: Redis/gRPC/shutdown/logging/config/Compose | ARCHITECTURE, SECURITY; 1/4/8/9 |
| 62–63: tests/races | Каждая фаза; итоговый прогон 10 |
| 64–66: OpenAPI/WS/README | Contracts Phase 0; executable docs с 1/4/8; обновлять постоянно |
| 67–69: dev auth/seed/security | SECURITY; 1, расширение seed 2/8/9 |
| 70–72, 77–78: acceptance/phases/workflow/DoD/start | Этот план; Phase 0 до skeleton; итоговая приёмка 10 |

## Сценарий итоговой приёмки

1. Из чистой копии запустить Docker Compose с документированной development configuration;
   Project/seed/dev keys создаются безопасно и повторяемо, секреты не попадают в Git/logs.
2. Получить JWT A/B, подключить WS, создать DIRECT, отправить message, получить realtime delivery
   и READ event; повторить тот же client_message_id без нового message.
3. Отключить B, отправить несколько сообщений, переподключить: полная упорядоченная history;
   восстановить также edits/deletes/reactions, проверить ещё одно устройство и потерю live hints.
4. Создать GROUP и private CHANNEL, добавить participants, назначить второго moderator,
   проверить channel write policy и изоляцию другого Project.
5. Проверить reply/reaction/edit/delete/forward/pin/image/search и backend flags.
6. Проверить blacklist, ban/unban/read-only, report, audit, API key revoke, webhook retry/HMAC,
   Echo Bot и gRPC scope isolation.
7. Открыть Admin/Demo/Swagger, пройти tests/race/security/fault/retention/cleanup сценарии.

Ни документация, ни успешная компиляция без tests не закрывают этот сценарий.
