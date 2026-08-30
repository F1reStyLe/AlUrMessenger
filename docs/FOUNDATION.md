# Phase 1 — Foundation acceptance

Подпункты 1.1–1.6 реализованы отдельно. Следующий этап — Conversations; сообщения,
WebSocket/gRPC, encryption/search, outbox/jobs и frontend не входят в эту приёмку.

| Проверка | Покрытие |
| --- | --- |
| `go test -race -count=1 ./...`, `go vet ./...`, `go build ./...` | Config/fail-fast/redaction, HTTP lifecycle, JWT matrix, patch validation, policies, OpenAPI refs/auth/path params, docs assets |
| `scripts/test-infrastructure.ps1` | Изолированные PostgreSQL/Redis/Kafka/MinIO; concurrent migrations/provisioning, tenant isolation, profile/ban, permissions, settings conflicts, audit rollback/grants, Redis/CORS, failures/recovery, Linux PID1 SIGTERM exit=0 |
| Development Compose | Nonroot/read-only API/worker; persistent volume upgrade migrations 1→2→3→4, repeat seed, profile/settings/audit API, readiness и CORS |
| Реальные Auth:8080 + Chat:18080 (2026-08-30, D32) | Login/refresh принимаются, logout отзывает оба access JWT в Chat; другая сессия работает. Auth admin остаётся Chat user; CLI grant/demotion видны с прежним JWT. Новый login сохраняет локальную роль. Fixture аккаунт и профиль удалены |
| OpenAPI spec validator 0.9.0 | Полная проверка OpenAPI 3.1, дополнительно к встроенным Go contract tests |
| Swagger UI | Локальные HTML/CSS/JS/spec, раскрытие операций и Try it out, отсутствие внешнего validator/CDN |
| govulncheck v1.7.0 | 0 достижимых и 0 в импортируемых packages; module-only GO-2026-5932 относится к неиспользуемому x/crypto/openpgp |

Swagger UI 5.32.14 закреплён с SHA-512 tarball, upstream LICENSE/NOTICE сохранены.
Все assets встроены в binary. Query-string overrides, external validator и persistent
authorization выключены. CSP разрешает scripts/connect только с текущего origin;
inline styles нужны Swagger, inline scripts/eval запрещены. Probes и docs публичные;
защищённые API требуют JWT независимо от UI. Production ingress может ограничить docs.

Seed создаёт только dev Project/admin/alice/bob. Admin — роль БД Chat; claims Auth игнорируются.
В remote JWT проверяется через Auth без positive cache, logout отзывает доступ Chat.
Конфигурация Auth валидируется до bind; неверная настройка не приводит к anonymous API.
Dev commands не работают в test/production. На несовместимой schema API/worker не стартуют.
PATCH null отклоняется, неизвестные actor/project/role поля не принимаются.
Migration 4 переводит существующие профили в user; admin назначается явно через user-role.
Прежние dev профили сохранены, автоматического повышения по имени или роли Auth нет.

Границы: Compose предназначен для local development; production TLS/ACL/HA
не прошли deployment acceptance. Remote обслуживает один настроенный Auth/Project на API;
RS256 доступен только как явный offline dev fixture, без отзыва сессии. Proxy headers пока
игнорируются, поэтому за reverse proxy IP quota общая. MinIO Community архивирован;
поддерживаемый production storage требует отдельного решения. Audit append-only для
runtime, но не immutable для доверенного DB operator. Расширенные audit filters/ban API
вводятся вместе с moderation/admin phase. Feature flags проверяются текущими policy APIs;
проверки отправки файлов/сообщений будут подключены к реальным use cases следующих фаз.

Воспроизводимые команды startup/seed/tests — [README](../README.md),
детали — [Development](DEVELOPMENT.md), [Identity](IDENTITY.md), [Policies](POLICIES.md).
