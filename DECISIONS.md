# Архитектурные решения

Статус: решения для новой реализации MVP из Phase 0; bootstrap реализуется в Foundation 1.1,
остальные решения вводятся вместе со своими фазами.
Менять решение новой записью с причиной и последствиями, не переписывать историю молча.
Точные dependency/image versions фиксируются при Foundation после проверки совместимости.

| ID | Решение | Причина, альтернативы и последствия |
| --- | --- | --- |
| D01 | Чистый старт; один modular monolith, API/worker из одного Go module | Прямое указание пользователя. Старые API/schema не поддерживаются; Git и внешние данные сохраняются. Микросервисы не нужны для MVP |
| D02 | net/http ServeMux, pgx/v5, goose, явный SQL | Минимум runtime-слоёв; Go router достаточен. Chi/sqlc возможны при конкретной пользе, ORM не добавляется. Driver остаётся в adapters |
| D03 | Project — единственное имя tenancy; UUID идентификаторы | Scoped repository methods + composite FK. RLS пока не единственная защита и не обязательна; его добавление не заменит service permissions |
| D04 | Внешний Auth подтверждает JWT/сессию онлайн; актуализировано D32 | Нет positive cache/fallback, Auth owns login/refresh/logout; RS256 остаётся только offline fixture |
| D05 | Project и локальные роли — operator CLI; dev-token/seed только development | Новый профиль user; Auth admin/имя/первый вход не дают прав Chat, см. D32 |
| D06 | DIRECT — одна пара разных users; GROUP — пишут участники; private CHANNEL — пишут moderators | Участники CHANNEL читают/реагируют/жалуются. Создатель GROUP/CHANNEL — moderator; нельзя снять последнего активного moderator без замены. Каналы не публичны |
| D07 | Чтение полной сохранившейся истории для активного membership; выход отзывает доступ | Простая модель MVP. Group/channel ban запрещает записи в этом conversation; Project ban запрещает все доменные записи. Banned сохраняет ранее разрешённое read-only |
| D08 | SENT после DB commit; DELIVERED по явному client ack; READ по user checkpoint | Запись в socket не доказывает получение. last_delivered_sequence/last_read_sequence монотонны и не выше текущего message_sequence; READ также повышает delivered |
| D09 | Message sequence отдельно от event sequence | Message sequence не может восстановить edits/deletes. Changefeed references + current state вместо полного event-sourced состояния. Resource version предотвращает применение старой версии |
| D10 | Effectively-once, transactional outbox, at-least-once Kafka | Per-aggregate ordering и идемпотентные consumers. Повтор ID с иным command fingerprint → 409. Distributed exactly-once не обещается |
| D11 | AES-256-GCM application encryption; key_version и AAD | Без E2EE: серверу нужны moderation/search. Ключи вне БД; raw content не попадает в outbox/Kafka/audit. Инфраструктурное disk encryption дополнительно, не вместо application encryption |
| D12 | Поиск по полным нормализованным словам через HMAC blind tokens и PostgreSQL GIN | Plaintext FTS index не хранится. Цена: утечка равенства/частот токенов, нет substring/fuzzy/ranking. Ключ поиска отдельный, derived per Project. Компромисс явно описан в SECURITY |
| D13 | WS first auth frame через TLS, затем доступ к командам | Browser не требует JWT query string. До auth нет подписок/данных; 5 секунд на handshake, небольшой frame limit. Origin allowlist и rate limits обязательны |
| D14 | Cross-instance Redis hints + durable PostgreSQL replay | Redis Pub/Sub не durable: API сверяет event cursor даже при открытом соединении. Это дополняет Kafka router и сохраняет recovery после потери Redis |
| D15 | Удаление сообщений soft delete; reply — reference; forward — независимый encrypted snapshot | После delete оригинал не выдаётся. Forward не ломается после удаления источника; можно переслать только доступный живой источник и писать в доступный target, в пределах Project |
| D16 | Upload через backend streaming, download через короткий presigned URL | Проверка magic bytes/MIME/extension/декодирования до ready. PNG/JPEG/WebP в MVP, SVG/GIF не принимаются. Bucket private; URL до 60 секунд, после выдачи немедленный отзыв не гарантируется |
| D17 | Retention 365 дней от created_at; project override в глобальных пределах | Edits не продлевают жизнь. Очистка связей/files/job retry безопасна; exact-once после истечения срока dedup не гарантируется. Это граница контракта, а не скрытое удаление dedup |
| D18 | API keys: 256 случайных бит, SHA-256 hash, prefix, scopes | Высокоэнтропийный secret показывается один раз. Service/bot actor не подменяет JWT user; raw key не хранится. Bot допущен только в разрешённые conversations |
| D19 | Webhooks HTTPS/HMAC, durable deliveries и retries, content-free events | SSRF protection и ограничение ответа. HMAC secret хранится encrypted: hash-only невозможен для подписи. Получатель дедуплицирует event_id; семантический порядок webhook не гарантируется |
| D20 | Audit append-only через application API; запись вместе с административной мутацией | Не логировать secrets/content. Project admin может читать audit, но не редактировать. DB operator технически имеет доступ; immutable compliance storage не обещается |
| D21 | Feature flags проверяются в backend на каждом use case | UI не граница безопасности. max_upload_size ≤ global cap; остальные limits также bounded. Отключение записи функции не удаляет ранее созданные данные |
| D22 | Отдельный простой Admin и Demo на Vue/TS/Vite | Нативный WebSocket, без Socket.IO и тяжёлого UI framework по умолчанию. Пользовательские message types расширяемы, UI polish вторичен |

## Уточнения, обязательные для реализации

### Identity и полномочия

В remote внешний ID берётся из подтверждённого ответа Auth, Project — из server config.
`user`/`admin` хранится в Chat users.role; moderator относится только к будущему membership.
Claims Auth/JWT не повышают права. D32 заменяет прежний источник ролей из D29.
Bot — user actor с kind=bot; system — служебный actor. Обычный JWT не отправляет SYSTEM.
Admin API явно выделяет просмотр/модерацию приватного контента и журналирует такой доступ.

### Dedup и конкурентные policy changes

Fingerprint учитывает canonical payload, conversation, type, reply/forward/attachment IDs;
используется keyed HMAC с отдельным purpose, чтобы не раскрывать короткий plaintext через перебор.
Fingerprint key version хранится в dedup row: повтор проверяется тем же ключом, который сохраняется
до expiry всех связанных записей. Rotation не должна превращать повтор в конфликт. Таймстемпы запроса
и request_id не входят. Client_message_id — UUID, выдаваемый клиентом до первой попытки.
Параллельные одинаковые запросы ждут unique constraint и возвращают один message_id/sequence.

Policy checks внутри транзакции используют общий lock protocol: Project policy, actor,
idempotency, conversations, memberships, messages/attachments — в документированном стабильном
порядке, UUID внутри уровня сортируются. Send берёт shared policy locks; ban/flags/membership
changes — конфликтующие exclusive locks. Уже прошедшая транзакция может закончиться до ban;
после commit ban новая запись не разрешается. Deadlock/serialization retry ограничен и повторяет
весь use case с тем же client_message_id. Конкретные SQL locks проверяются concurrency tests.

### Search и encryption

Нормализация: Unicode NFC, case folding, разделение по не-буквам/не-цифрам, distinct full tokens.
AND всех query tokens, максимум 8 слов запроса; без prefix и морфологии. Message validation
ограничивает размер payload и число индексируемых токенов; нельзя молча не индексировать хвост.
Результат — доступные живые messages по sequence DESC с cursor. Decryption выполняется после
проверки прав; search tokens удаляются при soft delete и пересчитываются при edit.

Key rotation: payload и search key versions независимы. Новые записи используют active key,
чтение допускает retained versions. Blind index хранит version у токенов; поиск учитывает все
читаемые версии до окончания переиндексации. Старый ключ удаляется только после проверки
re-encryption/reindex и политики backup. Автоматизированная KMS rotation — будущее расширение.

### Retention и ссылки

Changefeed хранится не меньше project message retention; окна idempotency и consumer dedup
покрывают документированный период повторов. Idempotency default 365 дней; поздний повтор
после expiry может стать новой отправкой и не обещает сохранение старой идентичности.
Публичные ответы сообщают `dedup_expires_at`; клиент не повторяет старую очередь после этого срока.
Минимальный dedup TTL не меньше retry/replay horizon; конфигурация отклоняет нарушение.

Reply при недоступном исходнике возвращает reference/tombstone без content. Forward копирует
текст в новый encrypted payload. Для IMAGE forward создаётся отдельный логический attachment,
ссылающийся на тот же storage object: источник может истечь, пока forward ещё удерживает object.
После исчезновения всех живых references worker ставит object в pending_delete, повторяет удаление
MinIO и только затем завершает cleanup. Short-lived unused uploads очищаются отдельным job.

### Что ещё проверяется в Foundation

Конкретные Go/tool/image versions, Kafka/Redis/MinIO clients, default limits и retry intervals
проверяются по официальной документации при внедрении. Они не наследуются из удалённого проекта.
Существенное изменение принятого поведения/API оформляется отдельным решением до реализации.

## D23 — Foundation 1.1: toolchain и границы bootstrap (2026-08-30)

Go 1.27.0 закреплён в go.mod после проверки [официального списка релизов](https://go.dev/dl/?mode=json).
Каркас использует только standard library; pgx/goose и infrastructure clients добавляются в 1.2.
Один module `github.com/F1reStyLe/AlUrMessenger`, два небольших cmd и общая composition root internal/app.

Config читает только environment, без implicit .env/Viper/global config state. APP_ENV обязателен;
все существующие duration/size/address settings валидируются до listen, ошибки не содержат значений.
Пока введены только APP/LOG/HTTP; обязательность секретов и endpoints появляется при реальном adapter.
Development defaults loopback; production bootstrap также ограничен loopback до TLS deployment.

Probe HTTP worker слушает отдельный port. Nil readiness означает 503, не успех; worker не имитирует
Kafka/outbox jobs. Эти процессы доказывают lifecycle, а не готовность Chat Service.
Swagger UI остаётся 1.6; текущие endpoints уже имеют OpenAPI JSON, чтобы документация не отставала.

## D24 — Foundation 1.1: shutdown и безопасные diagnostics (2026-08-30)

Первый Ctrl+C/SIGTERM начинает bounded drain. Request contexts намеренно не наследуют отмену
signal context: иначе долгий запрос отменится до graceful завершения. После shutdown deadline
они отменяются, connections закрываются, exit code ненулевой. Новые requests отклоняет admission gate.
Реализация следует lifecycle [net/http.Server.Shutdown](https://pkg.go.dev/net/http#Server.Shutdown)
и [signal.NotifyContext](https://pkg.go.dev/os/signal#NotifyContext); WS/hijacked connections и jobs
потребуют собственной остановки в будущих фазах. Процесс не ждёт бесконечно handler, игнорирующий context.

Access logs содержат только method, route pattern, status, bytes, duration и проверенный request ID.
Raw URL/query/body/headers, panic values/stack и free-form net/http errors не логируются;
transport diagnostics сводятся к стабильному error_code. Это сознательный компромисс до введения
безопасных подробных infrastructure diagnostics: секреты не должны попадать в stdout.

## D25 — Foundation 1.2: adapters и administrative commands (2026-08-30)

Закреплены pgx/v5 v5.10.0, goose/v3 v3.27.3, go-redis/v9 v9.22.0, franz-go v1.21.6,
minio-go/v7 v7.3.0; версии проверены через Go module metadata и сборку Go 1.27.
Используются официальные clients: [go-redis](https://github.com/redis/go-redis),
[franz-go](https://github.com/twmb/franz-go), [minio-go](https://github.com/minio/minio-go).

Миграции — отдельный `cmd/migrate`, embedded SQL и [goose Provider с session advisory lock](https://pressly.github.io/goose/documentation/provider/).
Runtime не вызывает goose и не получает migration credentials. Первая миграция создаёт schema
chat и grants, без фиктивных Project/User tables до 1.4. Runtime использует read-only schema check;
неверная версия, в том числе будущая, закрывает startup/readiness.
Отдельный `cmd/minio-init` создаёт bucket только по явному вызову, не изменяет существующую policy.
MinIO runtime требует GetBucketPolicy и IAM rights одного bucket, но не SetBucketPolicy/CreateBucket.

Для секретов добавлены NAME/NAME_FILE с отказом при конфликте и bounded file read.
Production требует PostgreSQL verify-full, Redis TLS/password, Kafka SASL_SSL/SCRAM-SHA-256,
MinIO HTTPS. Небезопасные query TLS overrides не допускаются. Local fixture TLS не проверяет;
production provisioning/certificates/ACL не объявляются готовыми.

## D26 — Foundation 1.2: граница readiness и проверок (2026-08-30)

Пока нет бизнес-обработчиков, readiness обеих ролей консервативно требует все четыре adapters.
Это временное отклонение от будущей capability/degraded политики ARCHITECTURE: после появления
write paths Kafka outage не должен лишать возможности PostgreSQL/outbox commit. Переключение
политики выполняется вместе с реальными use cases, а не фиктивными фоновыми workers.
Kafka metadata check не доказывает topic ACL/consumer readiness; реальные produce/consume
проверены отдельно в integration fixture. MinIO bucket policy должна быть пустой; сложные IAM
условия не анализируются. Отдельно проверяется anonymous GET и запрет policy changes runtime.

HTTP drain и инфраструктурный cleanup имеют отдельные ограниченные budgets. Клиенты закрываются
после HTTP; будущие jobs/producer flush должны завершиться до этого. На текущем шаге собственных
business loops нет; закрываются SDK goroutines/connections. Liveness не зависит от сети.
Fixtures создают только новый Compose project со своими volumes, не используют чужие данные.

## D27 — MinIO Community: обнаруженный риск поддержки (2026-08-30)

[Официальный MinIO repository](https://github.com/minio/minio) архивирован и помечен как
неподдерживаемый. Технологию из ТЗ не заменяем без отдельного решения: S3 adapter реализован,
но legacy pinned server используется только в изолированных local tests. Production storage
нужно выбрать/подтвердить отдельно с учётом поддержки и security maintenance.
Production готовность такого MinIO не заявляется. Пользователь уведомлён о риске в ходе шага.

## D28 — Foundation 1.3: development deployment (2026-08-30)

Один multi-stage Dockerfile собирает команды Go 1.27 и помещает их в distroless static
nonroot image; оба base image закреплены digest. API/worker работают с read-only filesystem,
без Linux capabilities. Собственная healthcheck command исключает необходимость shell/curl.
Compose не публикует PostgreSQL/Redis/Kafka/MinIO на хост; HTTP доступен только через loopback.

Dev-init разрешён только при APP_ENV=development. Секреты создаются криптографически случайно
и не ротируются при повторном запуске; derived URLs должны совпадать. Каталог .local закрыт
mode 0700, secret files readable для непривилегированного контейнера. Для Windows применяются
ACL хоста. Используется [выдача Compose secrets отдельным сервисам](https://docs.docker.com/compose/how-tos/use-secrets/),
а не полный mount каталога с чужими credentials. Не выдаём такой development deployment за production.
## D29 — Foundation identity: RS256, trusted Project bindings, atomic provisioning

Историческое решение 1.4: источник идентичности/ролей заменён D32 по уточнению пользователя.

Принято в 1.4. Внешний Auth отвечает за `sub/project_id/roles`; Chat проверяет RS256,
issuer/audience из operator-managed Project, exp/nbf/iat и token_use=access (skew 30s).
Для MVP используется локальный RSA public key >=2048 bits; rotation через restart.
JWKS не требуется для текущего единого Auth trust domain, но понадобится перед подключением
независимых issuers. Заголовки JWT не выбирают URL/key source. Private key только dev-token.
Project CLI использует migration credentials; публичного provisioning endpoint нет.
Уникальность `(project_id, external_user_id)` и atomic upsert исключают дубли при первом входе.
Профиль не перезаписывается JWT claims. Actor IDs отсутствуют в PATCH DTO. Bans read-only;
проверка под Project shared lock/user lock вместе с изменением профиля. Runtime auth bindings
недоступны для UPDATE; UPDATE(updated_at) разрешён только как prerequisite row locks.
Seed/dev-token требуют development, seed не меняет существующие профили, roles не хранятся в users.
## D30 — Foundation policies, atomic audit and shared admission limits

Принято в 1.5. Project settings — typed partial update с expected_version (JSON string),
exclusive Project lock, повторной ban-проверкой и audit insert в той же transaction.
Profile writes берут shared Project lock; будущие message use cases обязаны соблюдать этот
порядок и проверять flags на snapshot своей transaction. Numeric caps: upload <=50 MiB,
retention 1..3650 дней; defaults 10 MiB/365. Flags не создают будущую функциональность.
Audit append-only для runtime SQL; UUID v7 keyset — ID order, не commit-ordered changefeed.
Расширенные audit filters и ban API остаются в фазе admin/moderation.
Rate limits — atomic Redis fixed 60s window, fail closed 503; IP до JWT, Project-user после.
Default 120/IP и 60/user, настройки bounded. Proxy headers пока не trusted: за proxy
IP quota общая, production должен отдельно определить trusted-proxy policy.
CORS exact origins, HTTPS в production, no cookies; OPTIONS без JWT, но с IP quota.
## D31 — Embedded Swagger and Foundation acceptance

Принято в 1.6. OpenAPI 3.1 и Swagger UI 5.32.14 встроены в API binary, assets vendored
из официального npm с проверкой SHA-512 и upstream licenses. Нет runtime CDN/Node/validator;
spec URL фиксирован, query overrides и persistAuthorization выключены, CSP scripts/connect self.
Docs публичные и только на API; их наличие не обходит JWT защищённых маршрутов.
Проверки объединяют Go contract tests, полный OpenAPI validator и browser Try it out.
В ходе contract review исправлены обязательный user_id для OPTIONS и PATCH null semantics.
Public key проверяется до bind, чтение ограничено 16 KiB. Phase 2 не начинается автоматически.

## D32 — Auth identity/session и локальные роли Chat (2026-08-30)

По явному уточнению пользователя общий Auth не знает прав приложений. После исправления
отзыва access JWT в Auth Chat принимает его JWT напрямую и проверяет через users/me
каждый раз: нет положительного кеша, второго bearer или своих login/refresh/logout.
Секрет HS256 остаётся в Auth. HTTPS в production, redirects/proxies отключены, timeout 2s,
ограничены bearer/response; отказ Auth даёт 401, недоступность/некорректный ответ — 503.

Из ответа Auth используется только ID. Роль user/admin хранится в users.role отдельно
для каждого Project; runtime SQL не может менять колонку. Migration 4 даёт всем старым
профилям user без догадок о прежнем admin JWT. user-role назначает существующему профилю
явную роль с operator credentials; нет первого admin по входу или переноса Auth admin.
Project/user lock order согласован с policy mutation, которая повторно проверяет роль
перед записью. Следующий запрос видит смену прав без обновления токена. Moderator будет
принадлежать membership, не global role. Новый dev seed admin получает роль в БД;
повтор seed не отменяет понижение. Параметр dev-token --admin удалён.

Текущий remote deployment обслуживает один операторский AUTH_PROJECT_ID и один Auth.
Все SQL/права tenant-scoped; произвольный клиентский Project не принимается. Любой активный
пользователь этого Auth получает обычный профиль: отдельного Project admission allowlist
пока нет. Не менять источник Auth при сохранённых профилях: numeric IDs не имеют namespace
поставщика. Multi-issuer/multi-Project routing требует отдельного контракта, не JWT ролей.
Поля Project issuer/audience сохраняются для dev-rsa, в remote не участвуют в проверке.
Offline dev-rsa допускается только development/test, никогда как fallback в production.

Удалены только неопубликованные draft internal/session и migration 5, которые дублировали
исправленный Auth. Схема Chat — 4. Отзыв не отменяет уже проверенный in-flight запрос.
CLI назначения ролей требует операционного журнала оператора; HTTP audit пока фиксирует
изменения settings, полноценный management API ролей относится к дальнейшей работе.
Контракт и команды: [Identity](docs/IDENTITY.md), [OpenAPI](api/openapi/openapi.json).

## D33 — Conversations и membership (Phase 2)

DIRECT/GROUP/CHANNEL реализованы через три tenant-qualified таблицы: conversations,
direct_pairs, conversation_members. PK нормализованной DIRECT-пары и pair advisory lock
дают единственный результат при конкурентном создании; triggers запрещают третьего
участника/leave/смену роли DIRECT и неполный creation commit. GROUP/CHANNEL private,
creator moderator; initial membership включён уже в 2.1, чтобы CRUD не был публичным.
DELETE conversation не добавлен: текущий API_DESIGN не содержит такой операции.

Список — ascending UUID v7 с cursor Project/user (для members также conversation).
Это стабильный порядок создания, не inbox ranking по последнему сообщению. Metadata edits
не перемещают строки. Максимум 100 initial/add users, 100 entries на страницу.
Moderator role/ban/leave защищены conversation row lock и last active moderator guard;
Project admin не обходит member/moderator. Muted только self, DIRECT поддерживает self-mute.
Human-only moderator пока соответствует human-only membership management API.

Audit пишется вместе с изменением. Leave не удаляет историю, rejoin ordinary member
сохраняет первоначальный joined_at/mute; version возрастает. Project/conversation ban
сохраняет read-only. Dev seed повторяем без восстановления удалённых membership/ролей.
Все endpoints документированы в OpenAPI; runtime DB не меняет tenant/identity columns.

## D34 — Messages, encryption и публикация (Phase 3)

TEXT/SYSTEM, SQL event log/outbox и idempotency receipt фиксируются атомарно. Conversation
row lock сериализует независимые message_sequence/event_sequence; client ID scoped по
Project/sender на 365 дней, keyed canonical fingerprint отклоняет изменённый retry с 409.
Окно dedup указано в ответе; истёкший контент не восстанавливается повтором.
AES-256-GCM + AAD связывает payload с Project/conversation/message/version. HKDF разделяет
Projects и назначения encryption/search/fingerprint. Ключи вне БД, старые версии сохраняются.
Blind HMAC index поддерживает whole-word Unicode AND search без plaintext-копии; утечки
равенства/частоты внутри Project приняты и описаны в docs/MESSAGES.md.
Worker публикует head aggregate с ack=all; повтор после потерянного ack сохраняет event_id.
Consumer проверяет canonical PG event, Redis publish, затем durable inbox, затем Kafka commit.
Дубликат Redis hint безвреден; очередность/содержимое доставки всегда задаёт PostgreSQL.

## D35 — Realtime, privacy и ограниченное состояние (Phase 4)

Использован [coder/websocket v1.8.15](https://pkg.go.dev/github.com/coder/websocket@v1.8.15),
с context-aware I/O, одним data writer и явным drain hijacked connections.
Протокол: auth frame вместо query/header JWT, exact Origin allowlist, 5s/8KiB handshake,
128KiB frames, 16 connections/user, 16 subscriptions/socket, bounded queues 16/64.
Raw JWT живёт только в Session. Проверка Auth на командах/выдаче/idle polling сохраняет
отзыв access token, цена — дополнительные Auth/PG обращения; positive cache не добавлен.
Перед отправкой перепроверяются membership и privacy, включая уже queued frames.

Replay — reference events из PG, independent event cursor, snapshot одной REPEATABLE READ
транзакцией. Неверный/gapped cursor требует resync, не молчаливого прыжка. Redis hints
ускоряют polling; periodic catch-up работает без них. HTTP и WS используют общие services.
READ/DELIVERED монотонны и не выше message_sequence; READ повышает delivered. Выключенный
read_receipts скрывает peer receipts через sync.advance, но сохраняет собственное состояние
между устройствами. Так flag не ломает приватный read checkpoint.

Вместо отдельных ephemeral online/offline и typing started/stopped дельт выбраны ограниченные
presence.state/typing.state snapshots. Presence watch до 100 IDs с текущим membership;
typing TTL 5s. UI заменяет состояние целиком, unavailable не равно offline. Redis connection
lease 75s продлевается только после pong; last_seen flush каждые 30s, до 256 markers.
Ограничения и точные поля отражены в docs/REALTIME.md; gRPC, bots и Phase 5–10 не реализованы.

## D36 — Reply, optimistic edit и redaction (шаг 5.1)

Нумерация локального Agents.md приведена к IMPLEMENTATION_PLAN.md: 5.1 содержит reply,
edit/delete; reactions/pins — 5.2, forward — 5.3. Обязательные flags/транспорты/recovery
для уже добавленных операций реализуются сразу, без ожидания итогового шага 5.4.

Reply — same-conversation reference без копирования текста (D15, раздел 21 ТЗ).
FK проверяет Project/conversation. После удаления источника самостоятельный ответ остаётся,
но GET источника возвращает tombstone. Старые send fingerprints сохраняются; новое optional
поле reply участвует в fingerprint. Matching retry не требует живого источника, но проверяет flag.

Edit — только текущий автор с правом записи; expected_version обязателен, конфликт 409.
Даже равный текст повышает version. Нельзя редактировать metadata/reply/TTL/sender/sequence
или SYSTEM. User delete также author-only; отдельная moderation операция остаётся Phase 7.
Delete физически очищает ciphertext и blind index, оставляя row/id/version для recovery.
Повтор не добавляет событие/версию, но заново проверяет flags/membership/bans. Удаление
возможно без ключа старого payload. Событие редактирования — message.updated по ТЗ.

GET/history/snapshot/dedup теперь возвращают retained deleted/expired tombstones, как требует
API_DESIGN; прежнее промежуточное исключение expired/404 в Phase 3 заменено. Поиск и новые
reply используют только live rows. Физический purge и независимый dedup tombstone после него
по-прежнему Phase 10. Уже выданный клиенту текст невозможно отозвать из его локальной памяти.

Stored events/outbox/Kafka остаются reference-only. REST/WS replay читает current message
в одном snapshot с event high watermark; старый created/updated получает текущий tombstone.
WS writer заново гидратирует очередь и acknowledgements, чтобы не выдать устаревший body.
Клиент заменяет DTO целиком по current resource_version; исторический reference version
используется только как metadata события. Event sequence и message sequence не смешиваются.
Равная версия expired tombstone возможна по времени TTL без mutation event; клиент учитывает
expires_at/status, а delete всегда повышает version. Точные схемы — OpenAPI 0.10.0.

Проверка 5.1 выявила зависимость outbox retry от расхождения часов PostgreSQL и worker:
One сравнивал DB deadline с локальным time.Now(), хотя Batch и backoff использовали DB clock.
Теперь One также проверяет next_attempt_at через clock_timestamp() в PostgreSQL. Это сохраняет
единый источник времени для очереди и не меняет порядок событий, backoff или ack contract.

## D37 — Reactions и pins (шаг 5.2)

Статус: принято. Реакции принадлежат authenticated actor: unique(Project,message,user,emoji).
Все активные human/bot members, включая читателей CHANNEL, могут добавить/снять свою реакцию;
общие bans, allow_reactions и allow_bots проверяются внутри transaction и на повторах.
Pins — отдельные rows; только moderator GROUP/CHANNEL, без обхода Project admin и без DIRECT.
allow_pin блокирует добавление/снятие, но существующее состояние остаётся доступно для чтения.

Один emoji определяется pinned [Unicode Emoji 16.0 data](https://www.unicode.org/Public/emoji/16.0/emoji-test.txt).
Сгенерированная таблица принимает fully-qualified и перечисленные Unicode presentation aliases,
нормализует к fully-qualified. Skin tones/ZWJ/flags/keycaps поддержаны, произвольные strings,
отдельные modifiers, невалидные последовательности и несколько emoji отвергаются. Версия данных
явна; runtime не обращается к сети. Генератор scripts/generate-emoji.ps1 и Unicode license сохранены.
Альтернатива range/regexp отвергнута: она не проверяет целостность составных emoji.

Message.reactions — counts по canonical emoji (decimal strings), без неограниченного списка
users. До 32 разных emoji на сообщение; число users для существующего emoji не ограничивается.
До 100 live pins на conversation; существующий pin можно повторить при достигнутом лимите.
Превышение лимитов даёт 400 INVALID_REQUEST. Ограничения обеспечиваются conversation lock.
Эти защитные пределы ограничивают размеры DTO/snapshot, не являются project feature flags.

Reaction/pin mutations повышают общий message.resource_version, не меняя sequence/edited_at/body.
Повтор существующего add и отсутствующего remove не создаёт version/event. Поэтому edit с версией
до reaction/pin получает VERSION_CONFLICT: клиент перечитывает актуальный Message. Это простая
единая версия для восстановления, без отдельных счётчиков/частичного merge.

События reaction.created/deleted и message.pinned/unpinned — reference-only (message_id,
message_sequence, resource_version). Вместо целевых исторических delta fields из Phase 0 клиент
получает актуальный payload.message через authorized replay и заменяет resource целиком.
Это не изменение опубликованного Kafka event типа: новые типы введены только в 5.2.
REST реакция и WS ack возвращают полный Message. Pin PUT возвращает Pin, DELETE — 204;
pin commands остаются REST, WS распространяет events по WEBSOCKET_PROTOCOL.

Soft delete очищает associations в той же transaction, единственное message.deleted
инвалидирует relations. Старые events тоже гидратируют tombstone, без прежнего relation state.
Expired messages немедленно скрывают reactions/pin; physical cleanup — Phase 10. Add к terminal
message даёт 404, remove возвращает текущее terminal состояние без новой версии/события.
Snapshot.pins содержит все live message IDs, включая вне последних 50 messages; GET pins
возвращает references с единым snapshot_event_sequence. Project/conversation composite FK
дублируют application boundary; runtime не может UPDATE association owner/reference.

## D38 — Независимый TEXT forward (шаг 5.3)

Статус: принято. Forward — взаимоисключающий вариант общего send command. До commit сервис проверяет
allow_forward, write access target и read access к живому source внутри Actor Project. Недоступный
source скрывается как 404; SYSTEM запрещён, IMAGE отложен до attachment lifecycle Phase 6.

Новый target message получает собственные UUID/sequence/sender/TTL, ciphertext/nonce/AAD и search
index. Из source копируется только TEXT. В authenticated payload сохраняются immediate source ID и
root original sender `{id,display_name}`; source conversation, metadata, reply, reactions и pin не
раскрываются и не копируются. Повторный forward сохраняет root attribution. Profile edit, source edit,
soft delete и physical purge не меняют snapshot. Composite Project FK хранит operational lineage,
а `ON DELETE SET NULL (forwarded_from_message_id)` очищает только nullable header при purge.

Fingerprint состоит из target conversation, client ID и source message ID. Matching retry повторно
проверяет target access/allow_forward, но не source: иначе принятый результат перестал бы быть
идемпотентным после законного удаления источника. Новая команда всё равно требует live source.
Stored `message.created` остаётся reference-only и добавляет только `forwarded:true`; sensitive body,
display name и source conversation доступны лишь после current authorization/hydration.

Forward берёт per-Project advisory lock до conversation locks. Это сознательное ограничение MVP:
редкая операция сериализуется внутри tenant, зато встречные A→B/B→A transactions не создают
циклический lock order. Обычные send/edit/delete/reaction/pin этот lock не используют.
