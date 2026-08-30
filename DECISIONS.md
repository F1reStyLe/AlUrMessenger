# Архитектурные решения

Статус: решения для новой реализации MVP, приняты на Phase 0; ещё не реализованы.
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
