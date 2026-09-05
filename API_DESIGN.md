# REST и Internal API

Проект контрактов Phase 0. Реализованы process/infrastructure probes и Identity API 1.4:
GET/PATCH `/api/v1/me`, GET `/api/v1/users`, GET `/api/v1/users/{user_id}`.
Точный текущий контракт/ошибки — OpenAPI; [auth/provisioning](docs/IDENTITY.md).
Шаг 1.5 добавил GET/PATCH `/admin/v1/project`, GET/PATCH `/admin/v1/feature-flags`
и GET `/admin/v1/audit-logs` (пока только cursor/limit); [policies](docs/POLICIES.md).
Реализуемые endpoints сопровождаются machine-readable OpenAPI
в `api/openapi/`; локальный Swagger UI на `/docs/api` и JSON `/docs/api/openapi.json` реализованы в 1.6.
Этот документ не означает готовность endpoint.
Реализованный scope расширен Phase 2–5: conversations/memberships, message send/history/search,
reply/edit/delete/reactions/pins/TEXT forward, read/delivered, events/snapshot и `/ws`.
Точные текущие ограничения и расхождения с будущими
контрактами ниже: [Messages](docs/MESSAGES.md), [Realtime](docs/REALTIME.md), OpenAPI.
WS использует те же application services и правила: [WEBSOCKET_PROTOCOL.md](WEBSOCKET_PROTOCOL.md).

## Уже реализовано: process/infrastructure probes, шаги 1.1–1.2

GET/HEAD `/health/live` → 200; GET/HEAD `/health/ready` → 200 при успешной проверке
PostgreSQL/schema version, Redis PING, Kafka metadata и существующего private MinIO bucket
без bucket policy; иначе 503. Четыре проверки параллельны, общий deadline — 1s.
Неверная конфигурация/недоступность dependencies при startup не допускает обслуживания HTTP.
API default address 127.0.0.1:8080; worker probes 127.0.0.1:8081. Error envelope и X-Request-ID
действуют на все текущие routes; unsupported method → 405/METHOD_NOT_ALLOWED, unknown route →
404/RESOURCE_NOT_FOUND. HEAD не возвращает body. Access logs не содержат raw URL/headers/body.
Точная спецификация: [OpenAPI JSON](api/openapi/openapi.json). Это infrastructure readiness, не бизнес-readiness и не
заглушки будущих chat endpoints: таких endpoints пока нет.

Probes доступны без JWT/API key и Project context; тело запроса и Content-Type не требуются.
OpenAPI описывает входной X-Request-ID, response headers, примеры и ошибки 413/500,
а также 404/405 в `x-routing-errors` (они не объявляют дополнительные поддерживаемые операции).
Превышение заявленного Content-Length даёт 413 до routing; во время drain новые запросы,
кроме liveness, получают 503. HEAD всегда без тела. Ошибки HTTP-парсера/лимита headers
и redirects net/http до middleware могут не иметь JSON envelope или X-Request-ID.

## Общие правила

- Public API `/api/v1`, административный `/admin/v1`. JSON UTF-8, UTC RFC3339 timestamps, UUID IDs.
- Message/event sequence и resource_version — десятичные строки во всех JSON DTO.
- Human: `Authorization: Bearer <access JWT>`. Service/bot: `Authorization: ApiKey <secret>`
  только на разрешённых этим типам actor маршрутах. Query token не принимается.
- Project извлекается из проверенного Actor, не выбирается произвольным client header.
  Remote использует AUTH_PROJECT_ID из server config, Auth подтверждает только user ID.
  User/admin определяется БД Chat на каждом запросе, не JWT claims; login/refresh/logout в Auth.
- `X-Request-ID` принимается только при допустимом формате/длине, иначе генерируется сервером;
  возвращается в response и ошибке. Он не обеспечивает idempotency.
- Body schema strict: unknown fields, недопустимые enum/UUID/размеры → 400/422.
  Content-Type для JSON — application/json. Сервер не выдаёт внутренние SQL/stack errors.
- Cursor для обычных списков opaque на основе `(created_at,id)`; сообщения — sequence cursor.
  Default limit 50, maximum 100; cursor bound к Project/filter/conversation и проверяется сервером.
- Несуществующий или чужой Project resource → 404 без подтверждения его существования.
  Известный доступный resource, но запрещённое действие → 403. Rate limit → 429 + Retry-After.
- Новое создание → 201 + resource; успешный идемпотентный повтор message → 200 + тот же результат.
  Update/read → 200; без тела → 204. Идемпотентная реакция/снятие pin не создаёт duplicate event.

```json
{
  "error": {
    "code": "FEATURE_DISABLED",
    "message": "This feature is disabled for the project",
    "request_id": "01900000-0000-4000-8000-000000000001",
    "details": {"feature": "allow_images"}
  }
}
```

Коды: INVALID_REQUEST, UNAUTHENTICATED, FORBIDDEN, RESOURCE_NOT_FOUND, USER_BANNED,
MEMBERSHIP_REQUIRED, FEATURE_DISABLED, RATE_LIMITED, PAYLOAD_TOO_LARGE, UNSUPPORTED_MEDIA,
IDEMPOTENCY_CONFLICT, VERSION_CONFLICT, LAST_MODERATOR, INVALID_CURSOR, RESYNC_REQUIRED,
DEPENDENCY_UNAVAILABLE, INTERNAL_ERROR. gRPC отображает те же domain errors в canonical status
с безопасными details; WS передаёт code/message/request_id без HTTP status.

## DTO

User: id, display_name, avatar_url, kind, status, last_seen_at. Внешний ID и ban metadata —
только self/privileged DTO. Conversation: id, type, title, avatar_url, created_by, created_at,
updated_at, last_message_id, message_sequence, event_sequence, resource_version и own_membership.
Membership: user_id, role, joined_at, left_at, muted, banned, last_read_sequence,
last_delivered_sequence, version. Admin DTO не переиспользовать как public DTO.

Message response: id, conversation_id, sender_id, type, content, sequence, resource_version,
created_at, edited_at, deleted_at, expires_at, reply, forward, attachments, reactions, pins.
content — расшифрованный объект только после авторизации. encrypted_content/nonce/key_version,
blind search tokens и DB metadata наружу не выдаются. Deleted/expired message — tombstone
с id/sequence/version/status; без прежнего content, attachments или reply snapshot.

## Пользователи и conversations

| Method/path | Request / response | Права |
| --- | --- | --- |
| GET `/api/v1/me` | → own User; auto-provision при первом валидном JWT | Human JWT |
| PATCH `/api/v1/me` | display_name, avatar_url → User | Self; не banned; avatar только допустимый HTTPS URL |
| GET `/api/v1/users` | cursor, limit → public User[] | JWT в своём Project; без внешних ID/секретов |
| GET `/api/v1/users/{user_id}` | → public User | Тот же Project |
| POST `/api/v1/users` | external_user_id, display_name?, avatar_url? → User | Project admin или service key users:write; duplicate → 409 |
| GET `/api/v1/conversations` | cursor, limit → conversations с own membership | Human/bot; только доступные |
| POST `/api/v1/conversations` | type, member_ids, title? → Conversation | Human; DIRECT ровно один other member; GROUP/CHANNEL moderator=actor |
| GET `/api/v1/conversations/{id}` | → Conversation | Active membership |
| PATCH `/api/v1/conversations/{id}` | title?, avatar_url?, expected_version → Conversation | GROUP/CHANNEL moderator |
| GET `/api/v1/conversations/{id}/members` | cursor, limit → Member[] | Active member |
| POST `/api/v1/conversations/{id}/members` | user_ids → Member[] | GROUP/CHANNEL moderator; DIRECT запрещено |
| PATCH `/api/v1/conversations/{id}/members/{user_id}` | role?, muted?, banned?, expected_version | muted — self; role/ban — moderator; последний moderator защищён |
| DELETE `/api/v1/conversations/{id}/members/{user_id}` | → 204 (left_at) | Self leave или moderator remove; не удаляет историю |
| GET `/api/v1/conversations/{id}/snapshot` | → conversation, members/checkpoints, pins, recent messages, snapshot_event_sequence | Active member; один REPEATABLE READ snapshot |

DIRECT lookup canonical и конкурентобезопасен: повтор creation той же пары → 200 существующего
conversation; новые arbitrary client IDs не влияют на uniqueness. Удаление/архивация DIRECT и
повторное создание после leave не разрешены в MVP: DIRECT leave endpoint возвращает 422,
чтобы не вводить неописанные механизмы восстановления пары. GROUP/CHANNEL можно покинуть.

## Сообщения, поиск, checkpoints

| Method/path | Request / response | Права и semantics |
| --- | --- | --- |
| GET `/api/v1/conversations/{id}/messages` | before_sequence или after_sequence, limit → items, next_cursor, has_more | Membership; оба cursor одновременно запрещены; items всегда ASC sequence |
| POST `/api/v1/conversations/{id}/messages` | MessageCommand → message, deduplicated, dedup_expires_at | Human/bot membership; CHANNEL moderator; общий send use case |
| GET `/api/v1/messages/{id}` | → Message/tombstone | Membership соответствующего conversation |
| PATCH `/api/v1/messages/{id}` | content, expected_version → Message | Автор; не SYSTEM; allow_edit; conflict → 409 |
| DELETE `/api/v1/messages/{id}` | → tombstone | Автор + allow_delete; отдельная admin/moderation операция может удалить нарушение независимо от пользовательского flag |
| GET `/api/v1/conversations/{id}/search` | q, before_sequence?, limit → matches, next_cursor | Только доступные живые messages; полные слова AND; sequence DESC |
| PUT `/api/v1/messages/{id}/reactions/{reaction}` | → current Message/reactions/version | Active member включая CHANNEL reader, allow_reactions; один Emoji 16.0, URL-encoded |
| DELETE `/api/v1/messages/{id}/reactions/{reaction}` | → current Message/reactions/version | Только собственная reaction, allow_reactions |
| GET `/api/v1/conversations/{id}/pins` | → items: Pin references, snapshot_event_sequence | Membership; до 100 live pins, один snapshot |
| PUT `/api/v1/conversations/{id}/pins/{message_id}` | → pin | Moderator, allow_pin; same conversation |
| DELETE `/api/v1/conversations/{id}/pins/{message_id}` | → 204 | Moderator, allow_pin |
| PUT `/api/v1/conversations/{id}/delivered` | sequence → own checkpoint | Active member; checkpoint max(old,new), не выше current sequence |
| PUT `/api/v1/conversations/{id}/read` | sequence → own read/delivered checkpoints | Active member, read_receipts; распространяется на все devices |
| GET `/api/v1/conversations/{id}/events` | after_event_sequence, limit → events, next_event_sequence, has_more | Durable catch-up; старый cursor → 409 RESYNC_REQUIRED |
| GET `/api/v1/presence` | user_ids (до 100) → status/last_seen | Только пользователи общих доступных conversations; presence_enabled |

Пример MessageCommand (sender/project не принимаются):

```json
{
  "client_message_id": "01900000-0000-4000-8000-000000000002",
  "type": "TEXT",
  "content": {"text": "Привет"},
  "reply_to_message_id": "01900000-0000-4000-8000-000000000005",
  "attachment_ids": []
}
```

IMAGE принимает content.caption и один ready attachment в MVP. TEXT не принимает attachments.
В реализованном шаге 5.1 MessageSend принимает TEXT/content/metadata и optional reply_to_message_id;
ненужный reply_to_message_id следует опустить (null отклоняется). attachment_ids — будущая Phase 6.
SYSTEM доступен только отдельному internal use case. Reply разрешён только в том же conversation
и проверяется allow_reply. Forward — отдельный command в том же POST:

```json
{
  "client_message_id": "01900000-0000-4000-8000-000000000003",
  "forwarded_from_message_id": "01900000-0000-4000-8000-000000000004"
}
```

Реализованный в 5.3 Forward command взаимоисключён с content/type/metadata/attachment_ids/reply,
даже если лишнее поле пустое/null. Сервер копирует доступный живой TEXT-источник в новый encrypted
payload и blind index, проверяет allow_forward, source read и target write в одном Project.
Original sender snapshot содержит внутренний user id и безопасное историческое display_name;
source conversation, metadata, reply/reactions/pin не копируются. Forward не меняется после edit,
soft delete или физической очистки источника. IMAGE forward остаётся Phase 6, SYSTEM запрещён.

Тот же client_message_id с другим fingerprint → 409 IDEMPOTENCY_CONFLICT, не новая отправка.
Повтор не обходит текущие права доступа. После физической очистки message dedup tombstone до TTL
возвращает исходные id/sequence/status=expired без content. Позднее dedup_expires_at гарантия не действует.

## Файлы и жалобы

| Method/path | Request / response | Правила |
| --- | --- | --- |
| POST `/api/v1/attachments` | multipart image → Attachment ready | **Реализовано 6.1**: ровно один file; membership не требуется до attach; human/bot с allow_images, effective size/MIME/extension/magic/full decoder/container validation |
| GET `/api/v1/attachments/{id}` | → safe metadata | Uploader пока unattached, иначе membership доступного message |
| POST `/api/v1/attachments/{id}/download-url` | → url, expires_at | Read-authorized capability issuance; не доменная запись, допустима banned read-only |
| POST `/api/v1/messages/{id}/reports` | reason, description → Report | Доступный message, незаблокированный human |
| POST `/api/v1/users/{id}/reports` | reason, description, conversation_id? → Report | Тот же Project, незаблокированный human; target/reference проверяются |
| GET `/api/v1/reports/{id}` | → own Report status | Reporter или admin; без внутренних review details для reporter |

Download URL не логируется/не кешируется shared cache, TTL ≤ 60 секунд. Удаление/leave не отзывает
уже выданный URL мгновенно. Upload binary не проходит через WS/JSON и не хранится в PostgreSQL.

## Admin API

Во всех строках требуется подтверждённая Auth идентичность с локальной ролью Project admin
или API key с admin scope, разрешённый для admin use cases (последний пока не реализован).
Все изменения и чувствительное чтение контента записываются в audit. Admin не может менять Project
context, ключи серверного шифрования или глобальные security caps через project settings.

| Группа | Endpoints / результат |
| --- | --- |
| Dashboard | GET `/admin/v1/dashboard` — агрегированные счётчики своего Project |
| Users | GET `/admin/v1/users`, GET `/admin/v1/users/{id}`, PUT/DELETE `/admin/v1/users/{id}/ban` |
| Conversations | GET `/admin/v1/conversations`, GET `/admin/v1/conversations/{id}`, GET `/admin/v1/conversations/{id}/messages`; CHANNEL фильтруется type |
| Moderation | DELETE `/admin/v1/messages/{id}` с reason; обычный moderator использует DELETE `/api/v1/conversations/{id}/moderation/messages/{message_id}` с reason |
| Reports | GET `/admin/v1/reports`, GET/PATCH `/admin/v1/reports/{id}`; transitions OPEN→REVIEWING→RESOLVED/REJECTED |
| Blacklist | GET/POST `/admin/v1/blacklist`, PATCH/DELETE `/admin/v1/blacklist/{id}` |
| API keys | GET/POST `/admin/v1/api-keys`, POST `/admin/v1/api-keys/{id}/revoke`, POST `/admin/v1/api-keys/{id}/rotate`; secret только в create/rotate response |
| Flags/settings | GET/PATCH `/admin/v1/project`, GET/PATCH `/admin/v1/feature-flags`; expected_version для updates |
| Webhooks | GET/POST `/admin/v1/webhooks`, GET/PATCH/DELETE `/admin/v1/webhooks/{id}`, POST `/admin/v1/webhooks/{id}/rotate-secret` |
| Deliveries | GET `/admin/v1/webhooks/{id}/deliveries`, POST `/admin/v1/webhook-deliveries/{id}/retry`; retry использует тот же event_id |
| Bots | GET/POST `/admin/v1/bots`, PATCH `/admin/v1/bots/{id}`, PUT/DELETE `/admin/v1/bots/{id}/conversations/{conversation_id}` |
| Audit | GET `/admin/v1/audit-logs` с actor/action/resource/time filters и cursor; нет update/delete API |

Provisioning Project/admin — operator CLI, не unauthenticated HTTP endpoint. Scope нельзя
самостоятельно расширить при rotation. API key revoke действует на новые операции, активные
bot sessions закрываются/перепроверяются по revocation policy.

## Flags и начальные limits

По умолчанию включены allow_images, allow_reactions, allow_edit, allow_delete, allow_forward,
allow_reply, allow_pin, typing_enabled, presence_enabled, read_receipts. allow_bots/allow_webhooks
по умолчанию false; dev seed включает их явно. Если flag отключён, пользовательские операции
записи функции → 403; старые сообщения/изображения не исчезают. Presence/read signals при
отключении не публикуются; admin moderation не отключается пользовательским allow_delete.

Начальные configurable bounds: text 16 KiB UTF-8, metadata 8 KiB, image upload 10 MiB,
WS frame 128 KiB после auth и 8 KiB до auth, JSON request 1 MiB, search 8 query words/2048 tokens
на message, history limit 50/100. Global caps ограничивают Project overrides.
Retention default 365 дней; изменение настройки применяется к новым сообщениям. Retroactive
пересчёт срока — отдельная операторская операция, не побочный эффект PATCH settings.

## Внутренний gRPC

Package `chat.internal.v1`. Transport mTLS + сопоставление service identity со scopes/Project allowlist.
project_id обязателен в запросах и проверяется против service identity; не доверять самому полю.
RPC: GetUser, GetConversation, CreateSystemMessage, SendBotMessage, BanUser. Последние два требуют
bot identity/allowlist и admin permission соответственно. Каждая запись использует общие policies,
idempotency, outbox и audit, не прямой INSERT. UUID — string, sequence — uint64 в protobuf.

Read methods возвращают только разрешённый DTO; GetUser не выдаёт secrets, GetConversation не
открывает приватный content без capability. Deadline обязателен, retries только с idempotency keys.
Полное дублирование REST через gRPC не планируется. `.proto` добавляется с реализацией Phase 8.
