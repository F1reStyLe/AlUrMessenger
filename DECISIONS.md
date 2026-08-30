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
| D04 | Внешний JWT: асимметричная подпись, local public key/JWKS через verifier | MVP RS256 allowlist; introspection позже. Проверять iss/aud/exp/sub/project_id; issuer binding хранится для Project. Не принимать произвольные jku/x5u URL |
| D05 | Project provisioning — operator CLI; dev-token/seed только development | Не создавать публичный регистрационный/Auth Service. Global admin claim действует внутри своего Project, не даёт cross-project полномочий |
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

JWT `sub` — внешний ID, `project_id` — внутренний Project UUID. Разрешённый issuer/audience
связан с Project при provisioning. Claim roles: `user`/`admin`; moderator хранится только в membership,
а не принимается как право на все каналы из JWT. Произвольные claims не повышают права.
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
