# MASTER PROMPT: UNIVERSAL CHAT SERVICE

Ты выступаешь как senior backend engineer / software architect.

Необходимо спроектировать и поэтапно реализовать самостоятельный универсальный Chat Service, который может интегрироваться в различные внешние продукты.

Основная ценность сервиса — backend и API. Frontend нужен только как административная панель и демонстрационный клиент для проверки возможностей сервиса.

Сервис должен быть независимым от конкретного продукта. Первый потенциальный сценарий использования — внутреннее общение внутри CRM компании, но архитектура не должна быть привязана к CRM.

Не пытайся реализовать всё за один этап. Проект должен строиться небольшими законченными фазами, каждая из которых запускается, тестируется и документируется.

---

# 1. Главная цель

Разработать отдельный универсальный backend-сервис чата со следующими возможностями:

* личные диалоги;
* групповые чаты;
* приватные каналы;
* пользователи;
* роли;
* модераторы каналов;
* боты;
* WebSocket real-time взаимодействие;
* REST API;
* внутренний gRPC;
* Kafka event bus;
* история сообщений;
* редактирование сообщений;
* удаление сообщений;
* ответы;
* пересылка;
* реакции;
* emoji;
* изображения;
* закреплённые сообщения;
* статусы доставки и прочтения;
* online/offline;
* last seen;
* typing;
* синхронизация между устройствами;
* поиск внутри конкретного диалога;
* webhooks;
* административная панель;
* API keys;
* feature flags;
* blacklist слов;
* жалобы;
* блокировка пользователей;
* audit log;
* MinIO для файлов.

Архитектура должна позволять в дальнейшем добавить:

* видео;
* документы;
* голосовые сообщения;
* rich links;
* push notifications;
* отдельный Notification Service;
* дополнительные типы сообщений;
* дополнительные роли;
* дополнительные backend-сервисы.

---

# 2. Технологический стек

Backend:

* Go;
* PostgreSQL;
* Redis;
* Kafka;
* MinIO;
* WebSocket;
* REST;
* gRPC.

Frontend:

* Vue 3;
* TypeScript;
* Vite.

Не использовать сложный UI-фреймворк без необходимости.

Frontend вторичен относительно backend.

Infrastructure:

* Docker;
* Docker Compose.

На MVP Kubernetes не нужен.

---

# 3. Архитектурный подход

Использовать modular monolith для основной Chat API на первом этапе.

Не дробить Chat Service преждевременно на множество микросервисов.

При этом модули должны иметь явные границы, чтобы в будущем отдельные части можно было вынести в самостоятельные сервисы.

Рекомендуемые модули:

* auth;
* users;
* conversations;
* channels;
* messages;
* reactions;
* attachments;
* websocket;
* presence;
* moderation;
* reports;
* bots;
* webhooks;
* integrations;
* api-keys;
* feature-flags;
* audit;
* search;
* admin.

Отдельными инфраструктурными компонентами являются:

* PostgreSQL;
* Redis;
* Kafka;
* MinIO.

В будущем отдельно появится:

* Notification Service.

В текущем MVP Notification Service реализовывать не нужно, но события для него должны быть предусмотрены.

---

# 4. Multi-project архитектура

Chat Service должен поддерживать подключение нескольких независимых приложений.

Ввести сущность:

Project

или

Application.

Используй один термин последовательно во всём проекте.

Каждая интеграция должна иметь собственный Project.

Все сущности должны быть логически привязаны к project_id:

* users;
* conversations;
* channels;
* messages;
* API keys;
* webhooks;
* feature flags;
* moderation rules;
* audit log;
* reports.

Пользователи одного Project по умолчанию не должны видеть ресурсы другого Project.

Архитектура должна исключать accidental cross-project data access.

project_id должен входить в проверки доступа на уровне repository/service layer.

---

# 5. Авторизация

Chat Service не занимается регистрацией и основной авторизацией пользователей.

Авторизацией занимается внешний Auth Service.

Chat Service получает JWT.

Из JWT необходимо получать минимум:

* user ID;
* project/application context;
* роли или claims, если они присутствуют.

ID пользователя Chat Service привязывается к ID пользователя из JWT.

Использовать external_user_id.

Нельзя доверять user_id, переданному клиентом отдельно, если пользователь уже идентифицирован JWT.

JWT должен валидироваться.

Архитектуру JWT verification сделать через интерфейс/абстракцию, чтобы можно было поддержать:

* локальную проверку public key;
* JWKS;
* introspection в будущем.

---

# 6. Пользователь

Пользователь может быть заранее создан через API.

Если пользователь ещё отсутствует, но приходит запрос с корректным JWT, Chat Service автоматически создаёт минимальный профиль.

Минимальный профиль:

* id;
* project_id;
* external_user_id;
* display_name;
* avatar_url;
* status;
* last_seen_at;
* created_at;
* updated_at.

При автоматическом создании:

display_name = случайный GUID/UUID-подобный идентификатор.

Пользователь позже может изменить имя.

Статусы:

* online;
* offline.

Presence хранить преимущественно через Redis.

last_seen_at периодически синхронизировать с PostgreSQL.

Не выполнять запись last_seen в PostgreSQL при каждом heartbeat.

---

# 7. Роли

На MVP нужны роли:

* admin;
* moderator;
* user.

Admin:

* имеет доступ к административной панели;
* видит пользователей;
* видит чаты;
* видит каналы;
* видит жалобы;
* может банить;
* может разбанивать;
* управляет blacklist;
* управляет API keys;
* управляет feature flags;
* видит audit log.

Moderator:

Moderator относится преимущественно к конкретному каналу/группе.

Создатель канала автоматически становится moderator.

Moderator может:

* назначить другого участника moderator;
* снять moderator;
* модерировать участников;
* закреплять сообщения;
* выполнять разрешённые действия модерации.

User:

обычный участник.

Не смешивать глобальную роль admin и роль moderator конкретного conversation/channel.

Предусмотреть:

global roles;

и

conversation membership roles.

---

# 8. Диалоги

Поддержать:

DIRECT — личный диалог.

GROUP — групповой чат.

CHANNEL — приватный канал.

На первом этапе правила создания диалогов отсутствуют.

Авторизованный пользователь может создавать доступные ему типы диалогов.

Для DIRECT необходимо предотвращать создание множества одинаковых личных диалогов между одной и той же парой пользователей внутри Project, если это не разрешено отдельным feature flag в будущем.

Conversation должен иметь примерно:

* id;
* project_id;
* type;
* title;
* avatar_url;
* created_by;
* created_at;
* updated_at;
* last_message_id;
* sequence;
* deleted_at.

ConversationMember:

* conversation_id;
* user_id;
* role;
* joined_at;
* left_at;
* last_read_sequence;
* muted;
* banned;
* metadata.

---

# 9. Сообщения

Поддерживаемый MVP:

TEXT;

IMAGE;

SYSTEM.

Архитектура типов сообщений должна позволять позже добавить:

VIDEO;

DOCUMENT;

VOICE;

LINK;

LOCATION;

другие типы.

Message должен содержать примерно:

* id UUID;
* project_id;
* conversation_id;
* sender_id;
* type;
* encrypted_content;
* sequence;
* reply_to_message_id;
* forwarded_from_message_id;
* created_at;
* edited_at;
* deleted_at;
* metadata.

Не использовать глобальный auto-increment ID сообщения как единственный публичный ID.

UUID предпочтителен.

---

# 10. Порядок сообщений

Порядок сообщений внутри одного conversation должен быть детерминированным.

Использовать монотонный sequence number внутри conversation.

Например:

conversation.sequence += 1

при создании сообщения.

Обеспечить конкурентобезопасное выделение sequence.

Нельзя определять порядок сообщений только по created_at.

Клиенты сортируют сообщения по sequence.

---

# 11. Гарантии доставки

Требование бизнеса:

сообщение после успешного принятия сервисом не должно потеряться;

после восстановления соединения клиент должен получить пропущенные сообщения;

порядок сообщений должен сохраняться.

Не заявлять физическую distributed exactly-once delivery.

Реализовать effectively-once semantics.

Клиент при отправке сообщения передаёт:

client_message_id UUID.

На уровне:

project_id + user_id + client_message_id

должна работать idempotency/deduplication.

Повторная отправка одного client_message_id не должна создавать второе сообщение.

Kafka может использовать at-least-once delivery.

Consumers должны быть idempotent.

Все события должны содержать event_id.

---

# 12. Transactional Outbox

Для событий Kafka обязательно применить Transactional Outbox Pattern.

Создание сообщения и создание outbox event выполняются внутри одной PostgreSQL transaction.

Пример:

BEGIN

INSERT message

UPDATE conversation sequence

INSERT outbox_event

COMMIT

Отдельный background worker публикует события из outbox в Kafka.

После подтверждения Kafka event отмечается published.

Не делать:

INSERT PostgreSQL → commit → Kafka publish

без outbox.

Это создаёт возможность потери событий.

---

# 13. WebSocket

На MVP real-time transport только WebSocket.

REST используется:

* для CRUD;
* загрузки истории;
* административных операций;
* настройки интеграции.

WebSocket:

* сообщения;
* delivery status;
* read status;
* typing;
* reactions;
* edits;
* deletion;
* presence;
* conversation updates.

Продумать protocol envelope.

Например:

{
"type": "...",
"request_id": "...",
"event_id": "...",
"conversation_id": "...",
"sequence": 123,
"payload": {}
}

Не привязывать WebSocket implementation напрямую к HTTP handlers.

Создать отдельные hub/session/connection abstractions.

---

# 14. WebSocket authentication

При подключении WebSocket пользователь должен проходить JWT authentication.

Не передавать JWT в обычном query string, если есть безопасная альтернатива.

Предусмотреть корректный authentication handshake.

После подключения WebSocket session знает:

* project_id;
* user_id;
* permissions;
* connection_id;
* device_id.

---

# 15. Multi-device

Один пользователь может иметь одновременно несколько активных клиентов:

* браузер;
* телефон;
* другая вкладка;
* другое устройство.

Redis должен хранить mapping:

user → connections.

Новое сообщение должно отправляться всем активным устройствам пользователя, где это необходимо.

Read status должен синхронизироваться между устройствами.

---

# 16. Recovery после reconnect

После переподключения клиент должен иметь возможность передать последний известный sequence.

Например:

last_known_sequence.

Backend должен вернуть missing events/messages.

Не рассчитывать исключительно на временный WebSocket connection.

Source of truth для истории — PostgreSQL.

Redis не является source of truth сообщений.

---

# 17. Статусы сообщения

Поддержать:

SENT;

DELIVERED;

READ.

SENT:

сервер успешно сохранил сообщение.

DELIVERED:

сообщение доставлено хотя бы одному активному connection получателя либо клиент подтвердил доставку — выбери модель и задокументируй её.

READ:

пользователь подтвердил чтение до определённого sequence.

Предпочтительно хранить:

last_read_sequence

на ConversationMember,

а не отдельную строку read-status для каждого сообщения каждого пользователя.

Для группового чата при необходимости отдельную детализацию можно добавить позже.

---

# 18. Typing

WebSocket events:

typing.start

typing.stop

Не сохранять typing events в PostgreSQL.

Использовать ephemeral state.

При необходимости Redis TTL.

---

# 19. Presence

Presence:

online;

offline;

last_seen.

Использовать heartbeat WebSocket.

Redis TTL должен защищать от зависших соединений.

При закрытии последнего connection пользователь становится offline.

Если connection исчез некорректно, TTL eventually переводит пользователя offline.

---

# 20. Реакции

Реакции должны быть отдельной сущностью.

MessageReaction:

* message_id;
* user_id;
* reaction;
* created_at.

На одну реакцию одного типа от пользователя не должно создаваться дубликатов.

---

# 21. Ответы

Сообщение может иметь:

reply_to_message_id.

При получении истории API может возвращать минимальный snapshot/reference исходного сообщения.

Не копировать полностью содержимое reply сообщения в новую запись без необходимости.

---

# 22. Пересылка

Использовать:

forwarded_from_message_id.

При необходимости в будущем можно добавить:

forwarded_from_conversation_id;

original_sender_snapshot.

Не делать жёсткой зависимости, которая ломается после удаления оригинального сообщения.

---

# 23. Редактирование

Хранить:

edited_at.

Создать событие:

message.updated.

На MVP хранение полной истории версий необязательно.

Архитектура должна позволять добавить MessageRevision позже.

---

# 24. Удаление

Использовать soft delete.

deleted_at.

После удаления обычный клиент не получает original content.

Создавать событие:

message.deleted.

Физическая очистка выполняется retention process позже.

---

# 25. Закрепление сообщений

Поддержать pinned messages.

Не использовать boolean pinned непосредственно в Message, если канал может иметь несколько закреплённых сообщений.

Лучше отдельная сущность:

ConversationPinnedMessage.

---

# 26. Изображения

Файлы изображений хранить в MinIO.

Backend должен проверять:

* MIME;
* размер;
* разрешённые extensions;
* project feature flags.

Не доверять Content-Type клиента без проверки.

Сохранять metadata attachment.

Attachment:

* id;
* project_id;
* uploader_id;
* storage_key;
* original_name;
* mime_type;
* size;
* status;
* created_at.

Не хранить binary data в PostgreSQL.

---

# 27. Ограничение размера файлов

Максимальный размер файла configurable.

Глобальное значение может задаваться через .env.

Project может иметь собственный override через feature/config.

Например:

GLOBAL_MAX_UPLOAD_SIZE

и

project.max_upload_size.

Project setting не должен превышать глобальный security limit.

---

# 28. MinIO

Использовать отдельный bucket или namespaced storage keys.

Пример:

project/{project_id}/attachments/{uuid}

Не использовать original filename как storage key.

Предусмотреть presigned URL архитектуру.

Не делать bucket public.

---

# 29. Поиск

MVP:

поиск сообщений только внутри конкретного conversation.

Использовать возможности PostgreSQL.

Не добавлять Elasticsearch/OpenSearch на MVP.

Поиск должен учитывать:

* project_id;
* conversation_id;
* permission пользователя.

Если содержимое сообщений хранится application-level encrypted и недоступно для PostgreSQL FTS, предусмотреть отдельный подход.

Для MVP допустим вариант:

* PostgreSQL encryption at rest на инфраструктурном уровне;
* application-level encrypted message payload;
* отдельный нормализованный searchable representation только если это согласуется с моделью безопасности.

Нельзя скрывать этот компромисс.

---

# 30. Шифрование сообщений

Все внешние подключения:

TLS.

MinIO:

private access.

Secrets:

только environment / secret configuration.

Сообщения должны быть зашифрованы at rest.

На MVP не реализовывать end-to-end encryption.

Причина:

серверу необходимы:

* поиск;
* blacklist;
* жалобы;
* модерация;
* потенциальная серверная обработка.

Создать EncryptionProvider interface.

Например:

Encrypt(ctx, plaintext)

Decrypt(ctx, ciphertext)

Encryption key не хранить в PostgreSQL рядом с ciphertext.

Master key приходит через environment/secret.

Предусмотреть key_version для будущей rotation.

---

# 31. Retention

История сообщений хранится 1 год.

Создать background retention job.

После достижения retention:

* soft-deleted/expired сообщения физически очищаются;
* связанные attachments удаляются из MinIO;
* реакции очищаются;
* связанные данные очищаются безопасно.

Retention = 365 дней.

Сделать configurable через environment/project settings, но default:

365 days.

---

# 32. Blacklist

Администратор может управлять blacklist через admin panel.

Blacklist scoped по Project.

Поддержать:

* добавить слово;
* удалить;
* включить;
* выключить.

При отправке сообщения выполняется проверка blacklist.

Политику реакции сделать configurable.

На MVP:

reject message.

Ответ API должен объяснять, что сообщение нарушает правила, но не обязательно раскрывать конкретное blacklist слово.

---

# 33. Жалобы

Пользователь может пожаловаться на:

* сообщение;
* пользователя.

Report:

* id;
* project_id;
* reporter_id;
* target_user_id;
* message_id;
* conversation_id;
* reason;
* description;
* status;
* created_at;
* reviewed_by;
* reviewed_at.

Статусы:

OPEN;

REVIEWING;

RESOLVED;

REJECTED.

---

# 34. Блокировка

Admin может:

ban user;

unban user.

Banned user не может:

* отправлять сообщения;
* создавать диалоги;
* выполнять write operations.

Решить отдельно, может ли banned user читать старую историю.

Для MVP разрешить read-only доступ, если security/business rules не требуют иного.

---

# 35. Bots

Боты обязательны архитектурно и функционально.

Bot должен быть отдельным типом actor/user.

Не привязывать bot реализацию к конкретному AI.

Bot может получать события сообщений и отправлять сообщения через внутренний API.

Нужны:

* bot identity;
* bot token/API credentials;
* permissions;
* список conversations, где bot разрешён.

Первый демонстрационный бот может быть Echo Bot.

Он нужен для проверки архитектуры.

---

# 36. Webhooks

Webhooks обязательны.

Project admin может зарегистрировать webhook endpoint.

WebhookSubscription:

* id;
* project_id;
* url;
* secret;
* enabled;
* subscribed_events;
* created_at.

Пример событий:

message.created

message.updated

message.deleted

message.read

reaction.created

reaction.deleted

conversation.created

conversation.updated

member.joined

member.left

user.online

user.offline

report.created

user.banned

user.unbanned

Webhook payload должен иметь:

* id/event_id;
* type;
* project_id;
* created_at;
* payload.

Webhook подписывать HMAC.

Например:

X-Chat-Signature.

Webhook delivery делать асинхронно.

Retry:

exponential backoff.

Webhook delivery должна быть idempotent со стороны получателя через event_id.

Хранить delivery attempts.

---

# 37. Отдельная документация Webhooks

Помимо основного Swagger/OpenAPI создать отдельную документацию по webhook events.

Swagger сам по себе ориентирован на входящие HTTP APIs, поэтому для event-driven contract желательно также добавить AsyncAPI.

Итого документация:

/docs/api

OpenAPI REST.

/docs/webhooks

описание webhooks.

/docs/asyncapi

Kafka/WebSocket/event contracts при возможности.

---

# 38. Kafka

Kafka использовать для backend events.

Пример topics:

chat.messages

chat.conversations

chat.presence

chat.moderation

chat.webhooks

Не создавать десятки topics без необходимости.

Message key для message events:

conversation_id.

Это помогает сохранить order внутри partition для конкретного conversation.

Все consumers:

idempotent.

Каждый event содержит:

* event_id;
* event_type;
* event_version;
* project_id;
* aggregate_id;
* occurred_at;
* payload.

---

# 39. Event versioning

Сразу предусмотреть versioning событий.

Например:

message.created.v1

или:

type = message.created

version = 1.

Нельзя без versioning менять существующий event contract.

---

# 40. Notification Service

Notification Service сейчас НЕ реализовывать.

Но публиковать события, на которых позже он сможет работать.

Например:

message.created.

Notification Service в будущем сможет отправлять:

* push;
* email;
* другие уведомления.

Не смешивать notification logic с Chat Service.

---

# 41. API Keys

Project должен поддерживать несколько API keys.

API key:

* id;
* project_id;
* name;
* prefix;
* key_hash;
* scopes;
* created_at;
* last_used_at;
* expires_at;
* revoked_at.

Никогда не хранить raw API key.

При создании показать secret только один раз.

Поддержать:

* create;
* revoke;
* rotate.

Scopes например:

chat:read

chat:write

users:read

users:write

webhooks:manage

admin.

---

# 42. Feature Flags

Project-level feature flags обязательны.

Минимум:

allow_images

allow_reactions

allow_edit

allow_delete

allow_forward

allow_reply

allow_pin

typing_enabled

presence_enabled

read_receipts

allow_bots

allow_webhooks

max_upload_size

message_retention_days.

Backend должен проверять feature flag.

Frontend не является security boundary.

Скрытие кнопки в UI недостаточно.

---

# 43. Security settings

Глобальные security settings из .env:

* allowed origins;
* CORS;
* API rate limits;
* upload size;
* JWT configuration;
* MinIO credentials;
* PostgreSQL;
* Redis;
* Kafka;
* encryption secrets.

Поддержать rate limiting.

Использовать Redis.

Rate limiting минимум:

* per IP;
* per user;
* per API key.

---

# 44. Audit log

Audit log обязателен.

Логировать административные действия:

* login/admin access при необходимости;
* ban;
* unban;
* blacklist changes;
* role changes;
* moderator assignment;
* API key create/revoke/rotate;
* webhook configuration;
* feature flags;
* project configuration;
* moderation actions.

AuditLog:

* id;
* project_id;
* actor_id;
* actor_type;
* action;
* resource_type;
* resource_id;
* metadata;
* ip;
* created_at.

Audit log не должен редактироваться через обычное API.

---

# 45. Админка

Создать простой frontend:

Vue 3 + TypeScript + Vite.

Основные страницы:

Dashboard;

Users;

Conversations;

Channels;

Reports;

Blacklist;

API Keys;

Webhooks;

Feature Flags;

Audit Log;

Project Settings.

UI функциональный и минималистичный.

Не тратить много времени на дизайн.

---

# 46. Demo Chat Widget

Создать простой демонстрационный frontend клиента.

Это НЕ универсальная UI библиотека.

Он нужен исключительно для проверки backend.

Возможности:

* login через тестовый JWT;
* список conversations;
* создание direct/group/channel;
* список сообщений;
* send;
* image upload;
* reply;
* reactions;
* edit;
* delete;
* forward;
* pin;
* typing;
* online;
* read status;
* reconnect.

Никакой кастомизации виджета на MVP не нужно.

---

# 47. REST API

Использовать versioning:

/api/v1/...

Пример групп:

/api/v1/users

/api/v1/conversations

/api/v1/conversations/{id}/members

/api/v1/conversations/{id}/messages

/api/v1/messages/{id}

/api/v1/messages/{id}/reactions

/api/v1/messages/{id}/reports

/api/v1/attachments

/api/v1/presence

/admin/v1/...

Не воспринимать этот список как окончательный.

Перед реализацией сформировать полноценный API contract.

---

# 48. Pagination

Для сообщений использовать cursor pagination.

Не использовать offset pagination для длинной истории.

Cursor желательно строить вокруг sequence.

Например:

before_sequence;

after_sequence;

limit.

---

# 49. Ошибки API

Создать единый format ошибок.

Например:

{
"error": {
"code": "MESSAGE_NOT_FOUND",
"message": "...",
"request_id": "...",
"details": {}
}
}

Нельзя отдавать внутренние database errors клиенту.

---

# 50. Database

PostgreSQL является source of truth.

Использовать migrations.

Предпочтительно goose.

Основные таблицы:

projects

users

conversations

conversation_members

messages

message_reactions

attachments

pinned_messages

reports

blacklist_entries

api_keys

webhook_subscriptions

webhook_deliveries

feature_flags/project_settings

audit_logs

bots

outbox_events

consumer_deduplication.

Добавить необходимые indexes.

Особое внимание:

project_id;

conversation_id;

sequence;

created_at;

external_user_id.

---

# 51. Constraints

Использовать database constraints там, где возможно.

Например:

UNIQUE(project_id, external_user_id)

UNIQUE(conversation_id, sequence)

UNIQUE(message_id, user_id, reaction)

UNIQUE(project_id, user_id, client_message_id)

или эквивалентная таблица idempotency.

Не полагаться только на Go validation.

---

# 52. Redis

Redis использовать для:

* presence;
* active connections metadata;
* typing TTL;
* rate limiting;
* возможно короткоживущий cache;
* distributed coordination при необходимости.

Redis не должен быть единственным хранилищем сообщений.

Потеря Redis не должна уничтожать историю.

---

# 53. gRPC

gRPC предназначен для внутренних backend integrations.

На первом этапе сделать минимальный Internal API.

Например:

GetUser

CreateSystemMessage

SendBotMessage

GetConversation

BanUser

или ограниченный набор реально нужных методов.

Не дублировать абсолютно весь REST API через gRPC.

---

# 54. Graceful shutdown

Все сервисы должны корректно завершаться.

При SIGTERM:

* перестать принимать новые HTTP requests;
* перестать принимать новые WebSocket connections;
* завершить или корректно закрыть active connections;
* закончить текущие Kafka operations;
* закрыть DB/Redis;
* корректно завершить workers.

---

# 55. Logging

Observability stack типа Prometheus/Grafana сейчас не нужен.

Но structured logging нужен обязательно.

Использовать Go slog или аналогичный structured logger.

Каждый request желательно коррелировать через:

request_id.

Kafka events:

event_id.

WebSocket:

connection_id.

Не логировать:

JWT;

API keys;

пароли;

raw encryption keys;

чувствительный message content без необходимости.

---

# 56. Configuration

Использовать .env.example.

Никаких credentials в git.

Разделить конфигурацию:

APP

HTTP

JWT

POSTGRES

REDIS

KAFKA

MINIO

ENCRYPTION

CORS

RATE_LIMIT

UPLOAD

RETENTION

WEBHOOK.

При старте валидировать обязательные параметры.

Fail fast при некорректной configuration.

---

# 57. Docker Compose

docker-compose должен поднимать минимум:

postgres

redis

kafka

minio

minio-init

chat-api

chat-worker

admin-web

demo-web.

Если Kafka требует дополнительные контейнеры — использовать современную разумную конфигурацию, предпочтительно KRaft без ZooKeeper, если выбранная версия это поддерживает.

Worker может пока использовать тот же Go codebase с отдельной командой.

Например:

cmd/api

cmd/worker.

---

# 58. Структура Go проекта

Сделать понятную production-oriented структуру.

Например:

cmd/

api/

worker/

internal/

auth/

user/

conversation/

message/

presence/

websocket/

attachment/

moderation/

webhook/

bot/

audit/

integration/

platform/

postgres/

redis/

kafka/

minio/

encryption/

http/

grpc/

config/

migrations/

api/

openapi/

asyncapi/

web/

Не делать абстракции ради абстракций.

Interfaces вводить на meaningful boundaries.

---

# 59. Dependency direction

Business/domain logic не должен зависеть от:

Gin/Echo/Chi;

PostgreSQL driver;

Kafka client;

MinIO SDK.

Transport и infrastructure располагаются снаружи application/domain layer.

Не нужно строить догматичную Clean Architecture с сотнями файлов.

Цель:

ясная архитектура и тестируемость.

---

# 60. HTTP framework

Для Go выбери простой production-friendly вариант.

Предпочтительно:

Chi

или стандартный net/http router ecosystem.

Аргументируй выбор в ARCHITECTURE.md.

---

# 61. PostgreSQL library

Предпочтительно pgx/v5.

Можно использовать sqlc, если это действительно упрощает типобезопасную работу.

Если выбираешь sqlc:

* добавить config;
* generation;
* Makefile/Taskfile;
* документацию.

Не использовать тяжёлую ORM без необходимости.

---

# 62. Testing

Нужны:

unit tests;

integration tests;

WebSocket tests;

repository tests;

idempotency tests;

ordering tests;

authorization tests;

project isolation tests.

Особенно важные тесты:

A user from Project A cannot access Project B.

Duplicate client_message_id does not create duplicate messages.

Concurrent send preserves unique sequence.

Reconnect returns missing messages.

Banned user cannot send.

Feature flag blocks disabled feature.

JWT user cannot impersonate another user.

Webhook retry works.

Outbox event eventually publishes.

Kafka consumer ignores duplicate event_id.

---

# 63. Race conditions

Запускать:

go test -race ./...

В первую очередь проверить:

WebSocket hub;

connection map;

presence;

conversation sequence allocation.

---

# 64. Swagger/OpenAPI

OpenAPI documentation обязательна.

Документация должна включать:

* endpoints;
* auth;
* request;
* response;
* error codes;
* pagination;
* examples.

Swagger UI должен запускаться локально.

---

# 65. WebSocket documentation

Отдельно документировать WebSocket protocol.

Для каждого event:

* direction client → server / server → client;
* payload;
* acknowledgment;
* error;
* idempotency;
* sequence semantics.

---

# 66. README

README должен позволять новому разработчику выполнить:

git clone

docker compose up

и получить работающую систему.

Документировать:

* architecture;
* ports;
* migrations;
* Kafka;
* MinIO;
* JWT dev mode;
* API docs;
* demo user creation;
* WebSocket;
* tests.

---

# 67. Development auth

Так как внешний Auth Service может отсутствовать локально, создать development-only механизм выпуска JWT.

Например:

dev-token command

или dev endpoint, доступный только при:

APP_ENV=development.

Никогда не активировать его автоматически в production mode.

---

# 68. Seed

Добавить dev seed:

Project;

Admin;

несколько Users;

Moderator;

Echo Bot;

несколько conversations.

Это должно позволять сразу открыть demo client.

---

# 69. Security

Обязательно учитывать:

JWT validation;

issuer/audience;

token expiration;

project isolation;

authorization;

CORS;

rate limiting;

file validation;

SQL injection;

XSS-safe API semantics;

API key hashing;

webhook HMAC;

encryption at rest;

secret management;

no sensitive logs.

---

# 70. Acceptance Criteria MVP

MVP считается рабочим, когда выполняется следующий сценарий.

Docker Compose запускается одной командой.

Создаётся Project.

Получаем JWT для User A и User B.

User A подключается по WebSocket.

User B подключается по WebSocket.

A создаёт direct conversation.

A отправляет сообщение.

B получает сообщение real-time.

B подтверждает read.

A получает read event.

B отключается.

A отправляет несколько сообщений.

B снова подключается.

B получает все пропущенные сообщения строго в порядке sequence.

Повтор отправки одинакового client_message_id не создаёт дубликат.

Создаётся group chat.

Добавляются участники.

Создаётся private channel.

Создатель становится moderator.

Moderator назначает второго moderator.

Работают:

reply;

reaction;

edit;

delete;

forward;

pin;

image upload.

Работает blacklist.

Работает ban/unban.

Работает report.

Работает webhook.

Работает Echo Bot.

Admin panel показывает основные сущности.

Swagger доступен.

Tests проходят.

---

# 71. Фазы разработки

РАБОТАТЬ СТРОГО ПО ФАЗАМ.

Не переходить к следующей большой фазе, пока текущая не компилируется и не имеет базовых тестов.

## Phase 0 — Architecture

До написания основной реализации создать:

ARCHITECTURE.md

DECISIONS.md

ERD или DB_SCHEMA.md

API_DESIGN.md

WEBSOCKET_PROTOCOL.md

EVENTS.md

SECURITY.md

IMPLEMENTATION_PLAN.md

Описать спорные решения.

После этого создавать skeleton проекта.

## Phase 1 — Foundation

Реализовать:

config;

logging;

PostgreSQL;

migrations;

Redis;

Kafka;

MinIO;

Docker Compose;

health endpoints;

JWT middleware;

Project;

User;

dev seed.

## Phase 2 — Conversations

Реализовать:

DIRECT;

GROUP;

CHANNEL;

membership;

roles;

moderators;

REST endpoints;

tests.

## Phase 3 — Messages

Реализовать:

message persistence;

conversation sequence;

client_message_id;

idempotency;

transactional outbox;

history;

cursor pagination;

tests concurrency/order.

## Phase 4 — WebSocket

Реализовать:

connections;

auth;

message delivery;

acks;

presence;

typing;

reconnect;

multi-device;

read status.

## Phase 5 — Message features

Реализовать:

reply;

reaction;

edit;

delete;

forward;

pin.

## Phase 6 — Attachments

MinIO;

image upload;

validation;

limits;

metadata;

secure download.

## Phase 7 — Moderation

Blacklist;

reports;

ban/unban;

moderator actions;

audit.

## Phase 8 — Integration platform

API keys;

webhooks;

HMAC;

webhook retries;

gRPC;

Kafka contracts;

Echo Bot.

## Phase 9 — Admin + Demo

Vue Admin;

Vue Demo Client.

Сначала functionality.

Не тратить время на сложный дизайн.

## Phase 10 — Hardening

Race tests;

integration tests;

security review;

project isolation;

reconnect scenarios;

Kafka duplicate scenarios;

MinIO cleanup;

retention.

---

# 72. Как работать с задачей

Перед каждой фазой:

1. Посмотри существующий код.

2. Определи минимальный scope текущей фазы.

3. Перечисли файлы/компоненты, которые собираешься изменить.

4. Реализуй.

5. Выполни formatter.

6. Выполни tests.

7. Исправь ошибки.

8. Обнови документацию.

9. Кратко зафиксируй результат фазы.

Не создавать заглушки вида:

TODO implement later

для функциональности, которая является acceptance criterion текущей фазы.

TODO допустим только для явно будущих возможностей.

---

# 73. Правило архитектурных решений

Если требование допускает несколько вариантов:

не задавай пользователю мелкие технические вопросы.

Самостоятельно выбери наиболее простой production-friendly вариант.

Зафиксируй его в DECISIONS.md.

Вопрос пользователю нужен только если решение существенно меняет бизнес-поведение или совместимость публичного API.

---

# 74. Не переусложнять

Не использовать без необходимости:

Kubernetes;

service mesh;

CQRS framework;

Event Sourcing;

Elasticsearch;

GraphQL;

несколько десятков микросервисов;

сложную distributed transaction систему.

Kafka events + PostgreSQL transactional outbox достаточно.

PostgreSQL остаётся source of truth.

---

# 75. Приоритеты

При конфликте требований использовать следующий порядок приоритетов:

1. Data integrity.

2. Security.

3. Project isolation.

4. Message ordering.

5. Idempotency.

6. Reliability.

7. API stability.

8. Simplicity.

9. Performance.

10. UI polish.

---

# 76. Производительность

Система на старте небольшая, но архитектура должна горизонтально масштабироваться.

Не оптимизировать преждевременно.

Однако нельзя создавать очевидные bottleneck решения.

Chat API должен потенциально поддерживать несколько instances.

Поэтому WebSocket routing нельзя строить так, будто всегда существует единственный процесс.

Для MVP Docker Compose может запускать одну instance API, но abstraction должна позволять перейти к нескольким.

Redis/Kafka могут использоваться в будущем для cross-instance delivery.

Задокументировать стратегию horizontal scaling.

---

# 77. Definition of Done

Каждая фаза считается завершённой только когда:

код компилируется;

Docker environment запускается;

migrations применяются;

tests текущей функциональности проходят;

API документирован;

нет известных критических race/data-loss проблем;

README/architecture docs актуальны.

---

# 78. Начало работы

СЕЙЧАС НЕ ПЫТАЙСЯ РЕАЛИЗОВАТЬ ВЕСЬ ПРОЕКТ СРАЗУ.

Начни с Phase 0.

Сначала:

1. изучи существующий repository, если он уже содержит код;

2. не удаляй существующие рабочие части без необходимости;

3. сформируй архитектуру;

4. создай документацию Phase 0;

5. создай предполагаемую структуру repository;

6. сформируй database model;

7. определи REST contracts;

8. определи WebSocket protocol;

9. определи Kafka/event contracts;

10. определи security model;

11. сформируй IMPLEMENTATION_PLAN.md;

12. только после этого переходи к Phase 1.

При каждом архитектурном решении ориентируйся на production-ready backend, но сохраняй разумную простоту MVP.

Главная цель проекта — не красивый демонстрационный frontend, а надёжное, документированное и расширяемое ядро универсального Chat Service.
