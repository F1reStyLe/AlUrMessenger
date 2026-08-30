# Phase 1 — Foundation acceptance

Подпункты 1.1–1.6 реализованы отдельно. Следующий этап — Conversations; сообщения,
WebSocket/gRPC, encryption/search, outbox/jobs и frontend не входят в эту приёмку.

| Проверка | Покрытие |
| --- | --- |
| `go test -race -count=1 ./...`, `go vet ./...`, `go build ./...` | Config/fail-fast/redaction, HTTP lifecycle, JWT matrix, patch validation, policies, OpenAPI refs/auth/path params, docs assets |
| `scripts/test-infrastructure.ps1` | Изолированные PostgreSQL/Redis/Kafka/MinIO; concurrent migrations/provisioning, tenant isolation, profile/ban, permissions, settings conflicts, audit rollback/grants, Redis/CORS, failures/recovery, Linux PID1 SIGTERM exit=0 |
| Development Compose | Nonroot/read-only API/worker; persistent volume upgrade migrations 1→2→3, repeat seed, dev-token, profile/settings/audit API, readiness и CORS |
| OpenAPI spec validator 0.9.0 | Полная проверка OpenAPI 3.1, дополнительно к встроенным Go contract tests |
| Swagger UI | Локальные HTML/CSS/JS/spec, раскрытие операций и Try it out, отсутствие внешнего validator/CDN |
| govulncheck v1.7.0 | 0 достижимых и 0 в импортируемых packages; module-only GO-2026-5932 относится к неиспользуемому x/crypto/openpgp |

Swagger UI 5.32.14 закреплён с SHA-512 tarball, upstream LICENSE/NOTICE сохранены.
Все assets встроены в binary. Query-string overrides, external validator и persistent
authorization выключены. CSP разрешает scripts/connect только с текущего origin;
inline styles нужны Swagger, inline scripts/eval запрещены. Probes и docs публичные;
защищённые API требуют JWT независимо от UI. Production ingress может ограничить docs.

Seed создаёт только dev Project/admin/alice/bob. Admin — роль JWT, не имя профиля.
Public key валидируется до bind; отсутствующий/неправильный ключ не приводит к anonymous API.
Dev commands не работают в test/production. На несовместимой schema API/worker не стартуют.
PATCH null отклоняется, неизвестные actor/project/role поля не принимаются.

Границы: Compose предназначен для local development, production TLS/ACL/HA и внешний Auth
не прошли deployment acceptance. Локальный JWT public key рассчитан на один доверенный
Auth domain; независимым issuers нужен отдельный key binding/JWKS. Proxy headers пока
игнорируются, поэтому за reverse proxy IP quota общая. MinIO Community архивирован;
поддерживаемый production storage требует отдельного решения. Audit append-only для
runtime, но не immutable для доверенного DB operator. Расширенные audit filters/ban API
вводятся вместе с moderation/admin phase. Feature flags проверяются текущими policy APIs;
проверки отправки файлов/сообщений будут подключены к реальным use cases следующих фаз.

Воспроизводимые команды startup/seed/tests — [README](../README.md),
детали — [Development](DEVELOPMENT.md), [Identity](IDENTITY.md), [Policies](POLICIES.md).
