# Модель данных

Domain schema ниже — проект Phase 0. Реализованы migrations 1–4: schema `chat`,
runtime privileges/goose tracking и `projects`/`users` с tenant uniqueness/FK.
Migration 3 добавляет project_settings и append-only audit_logs; runtime не меняет audit rows.
Migration 4 добавляет users.role (user/admin) и запрещает runtime INSERT/UPDATE этой колонки.
Migrations 5–6: conversations, direct_pairs, conversation_members и защита moderator/DIRECT.
Migration 7: encrypted messages, message_idempotency, message_search, conversation_events,
outbox_events, consumer_inbox и независимые message/event counters.
Migration 8: membership READ/DELIVERED checkpoints; last_seen_at из Foundation используется batch worker.
Имена/constraints реализованной схемы — в SQL migrations; остальные таблицы ниже остаются планом.
PostgreSQL — source of truth. [Текущая миграция](migrations/00001_foundation.sql).
Все UUID выдаются приложением; timestamp — timestamptz UTC. BIGINT sequences в JSON передаются
десятичными строками, чтобы JavaScript не терял точность. Soft-deleted/expired данные не выдаются
обычным API даже до физической очистки.

## Общие ограничения

- Каждая project-owned таблица содержит `project_id NOT NULL REFERENCES projects(id)`.
- Resource lookup всегда требует project_id. Для cross-table references использовать composite FK
  `(project_id, resource_id)` → `UNIQUE(project_id, id)`, а не только глобальный UUID.
- Message relations дополнительно проверяют conversation. Reply/pin/reaction обязаны относиться
  к тому же conversation; forward допускает другой conversation только того же Project с проверкой прав.
- Все размеры, sequence/checkpoints ≥ 0; read/delivered bounds к текущему conversation проверяются
  в транзакции. CHECK не подменяет cross-row authorization.
- FK по умолчанию RESTRICT/NO ACTION; физическую очистку выполнять явно в нужном порядке.
  Не применять широкие CASCADE к messages/files/audit. Идентичность пользователя не hard-delete в MVP.
- JSONB metadata ограничены размером и схемой. Не складывать туда plaintext messages, tokens и secrets.

## Таблицы identity/settings

| Таблица | Основные поля | Constraints / индексы |
| --- | --- | --- |
| projects | id, name, status, auth_issuer, auth_audience, policy_version, created_at, updated_at | PK id; issuer/audience из доверенного provisioning, не из запросов пользователя |
| users | id, project_id, external_user_id nullable для bot/system, kind, role, display_name, avatar_url, status, last_seen_at, banned_at, ban_reason, policy_version, created_at, updated_at | UNIQUE(project_id,id); UNIQUE(project_id,external_user_id); human требует external_user_id; role user/admin, default user, operator-only mutation |
| project_settings | project_id, flags JSONB, max_upload_size, message_retention_days, settings_version, updated_by, updated_at | PK project_id; typed validation и global bounds; defaults задокументированы в API_DESIGN |
| api_keys | id, project_id, actor_type, actor_id, name, prefix, key_hash, scopes, created_at, last_used_at, expires_at, revoked_at | UNIQUE(key_hash); index(prefix); actor whitelist, expiry; hash не выдаётся API |
| bots | id, project_id, user_id, name, enabled, metadata, created_at | UNIQUE(project_id,user_id); user.kind=bot проверяется service; credentials через api_keys |
| bot_conversations | project_id, bot_id, conversation_id, created_at | PK(project_id,bot_id,conversation_id); две composite FK; дополнительно активное membership |

Project roles admin/user хранятся в users.role; default user, изменение только оператором
через user-role. JWT и Auth roles не назначают права (D32). Поле moderator в users не вводится:
оно принадлежит будущему membership. users.status — snapshot, realtime presence берётся из Redis.

## Conversations и сообщения

| Таблица | Основные поля | Constraints / индексы |
| --- | --- | --- |
| conversations | id, project_id, type DIRECT/GROUP/CHANNEL, title, avatar_url, created_by, last_message_id, message_sequence, event_sequence, resource_version, policy_version, created_at, updated_at, deleted_at | UNIQUE(project_id,id); index(project_id,updated_at,id); counters >= 0 |
| direct_pairs | project_id, low_user_id, high_user_id, conversation_id | PK(project_id,low_user_id,high_user_id); CHECK low < high; UNIQUE(project_id,conversation_id); все composite FK |
| conversation_members | project_id, conversation_id, user_id, role member/moderator, joined_at, left_at, last_read_sequence, last_delivered_sequence, muted, banned, metadata, version | PK(project_id,conversation_id,user_id); index(project_id,user_id,left_at,conversation_id) |
| messages | id, project_id, conversation_id, sender_id, type TEXT/IMAGE/SYSTEM, encrypted_content, nonce, key_version, payload_version, sequence, resource_version, reply_to_message_id, forwarded_from_message_id, created_at, edited_at, deleted_at, expires_at, metadata | UNIQUE(project_id,id); UNIQUE(project_id,conversation_id,id); UNIQUE(project_id,conversation_id,sequence); index(project_id,conversation_id,sequence DESC); index(expires_at,id) |
| message_idempotency | project_id, sender_id, client_message_id, command_fingerprint, fingerprint_key_version, message_id nullable после purge, conversation_id, message_sequence, created_at, expires_at | PK(project_id,sender_id,client_message_id); index(expires_at); tombstone результата сохраняется до dedup expiry |
| message_search | project_id, conversation_id, message_id, search_key_version, tokens text[] | PK(project_id,message_id,search_key_version); GIN(tokens); index(project_id,conversation_id); только HMAC tokens |
| message_reactions | project_id, conversation_id, message_id, user_id, reaction, created_at | PK(project_id,message_id,user_id,reaction); message composite FK включает conversation; normalized Unicode emoji, bounded length |
| pinned_messages | project_id, conversation_id, message_id, pinned_by, created_at | PK(project_id,conversation_id,message_id); message composite FK; несколько pins |

Миграция 10 реализует message_reactions/pinned_messages. Runtime может INSERT/DELETE,
но не UPDATE association. Soft delete message очищает обе таблицы в одной transaction.
Reaction/pin mutations повышают message.resource_version; duplicate commands — no-op.
Пределы application layer под conversation lock: 32 разных emoji/message, 100 live pins/conversation.

DIRECT creation вставляет canonical pair внутри транзакции. При конфликте получает существующий
conversation, не создаёт две пары. Ровно два разных human/bot users в пределах Project; group/channel
membership обновляется отдельными use cases. Hard-delete pair не открывает путь к дубликату.

reply_to FK относится к тому же conversation. Forward provenance — nullable FK внутри Project:
при физическом удалении источника `ON DELETE SET NULL (forwarded_from_message_id)` обнуляет только
nullable header, не обязательный project_id; encrypted content/author snapshot сохраняется.
Сведения о приватном исходном conversation не раскрываются получателю forward. Reply/pin/last_message
references также обновляются до физического удаления. Последний message пересчитывается без expired/deleted.

`resource_version` начинается с 1 и повышается при content/delete и изменении message-level состояния
(reactions/pins). Mutable aggregate DTO возвращает version, чтобы replay не откатывал UI.
Membership version независимо защищает read/mute/role updates.

## Attachments и storage

| Таблица | Основные поля | Constraints / индексы |
| --- | --- | --- |
| storage_objects | id, project_id, storage_key, mime_type, size, sha256, status uploading/ready/pending_delete/deleted, created_at, updated_at | UNIQUE(project_id,id), UNIQUE(storage_key); keys project/{project_id}/attachments/{uuid}; size <= 50 MiB DB cap |
| attachments | id, project_id, uploader_id, object_id, original_name, mime_type, size, width, height, status uploading/ready/attached/deleted, created_at, expires_at | UNIQUE(project_id,id), UNIQUE(project_id,object_id); composite object/user FK; filename sanitized metadata, не storage path |
| message_attachments | project_id, conversation_id, message_id, attachment_id, position | PK(project_id,message_id,attachment_id); UNIQUE(project_id,attachment_id); composite FK; исходный attachment прикрепляется к одному message; forward создаёт новый logical attachment |
| storage_cleanup_jobs | id, project_id, object_id, state, attempts, next_attempt_at, lease_until, last_error_code, created_at | UNIQUE(project_id,object_id); index(state,next_attempt_at); никакого signed URL/секрета в last_error |

Message attach и pending_delete синхронизируются lock объекта. Pending-delete object не принимает
новые ссылки. Удаление binary не выполняется внутри DB transaction; durable cleanup переживает crash.
Состояние upload в MinIO и PostgreSQL согласуется повторяемым sweeper; orphan grace period configurable.

## Moderation, integrations и audit

| Таблица | Основные поля | Constraints / индексы |
| --- | --- | --- |
| blacklist_entries | id, project_id, normalized_word, enabled, created_by, created_at, updated_at | UNIQUE(project_id,normalized_word); policy reject в MVP; normalized matching как в DECISIONS |
| reports | id, project_id, reporter_id, target_user_id, message_id, conversation_id, reason, description, status OPEN/REVIEWING/RESOLVED/REJECTED, reviewed_by, reviewed_at, created_at | CHECK есть target_user_id или message_id; composite FK и target consistency; index(project_id,status,created_at,id) |
| audit_logs | id, project_id, actor_id, actor_type, action, resource_type, resource_id, metadata, ip, request_id, created_at | index(project_id,created_at DESC,id); append-only application access |
| webhook_subscriptions | id, project_id, url, encrypted_secret, secret_key_version, enabled, subscribed_events, created_at, updated_at | index(project_id,enabled); URL allow/deny policy до сохранения и на каждой доставке |
| webhook_deliveries | id, project_id, subscription_id, event_id, canonical_body, state, attempts, next_attempt_at, lease_until, created_at, delivered_at | UNIQUE(project_id,subscription_id,event_id); index(state,next_attempt_at); body только content-free event |
| webhook_attempts | id, project_id, delivery_id, attempt, started_at, duration_ms, status_code, error_code | UNIQUE(project_id,delivery_id,attempt); response body не сохраняется |

Report description потенциально чувствителен: application encryption тем же provider с отдельным
purpose/AAD; не индексировать plaintext. Audit metadata содержит только разрешённые служебные поля.
Report после message retention остаётся без original content: ссылка обнуляется, идентификатор
сохраняется в безопасном reference metadata. Автоматическое сохранение content как evidence не вводится.

## Changefeed, outbox и consumers

| Таблица | Поля | Constraints / назначение |
| --- | --- | --- |
| conversation_events | event_id, project_id, conversation_id, event_sequence, event_type, event_version, resource_id, resource_version, message_sequence nullable, payload JSONB, occurred_at, expires_at | UNIQUE(event_id); UNIQUE(project_id,conversation_id,event_sequence); index(expires_at); replay references, без content |
| event_streams | project_id, aggregate_type, aggregate_id, sequence | PK(project_id,aggregate_type,aggregate_id); counters для non-conversation aggregates |
| outbox_events | event_id, project_id, aggregate_type, aggregate_id, aggregate_sequence, event_type, event_version, payload, occurred_at, published_at, attempts, next_attempt_at, last_error_code | UNIQUE(event_id); UNIQUE(project_id,aggregate_type,aggregate_id,aggregate_sequence); partial pending index |
| consumer_deduplication | project_id, consumer_name, event_id, processed_at, expires_at | PK(project_id,consumer_name,event_id); local side effects и dedup marker в одной transaction |

Conversation event_sequence совпадает с aggregate_sequence его outbox events. Outbox payload
содержит references, а не encrypted_content/доступные для пересылки secrets. Durable conversation
mutation пишет основную сущность + changefeed + outbox атомарно. Изменение Project/user пишет
event_stream counter + outbox в той же transaction. Audit также включается, где обязателен.

Outbox worker сначала захватывает per-aggregate PostgreSQL advisory transaction lock, затем
выбирает самые ранние неопубликованные события ORDER BY aggregate_sequence. Если head отложен
backoff, нельзя публиковать следующий. Подтверждение Kafka предшествует отметке published/commit.
Network timeout ограничивает удержание lock; crash после Kafka ack допускает повтор того же event_id.

Retention не удаляет unpublished outbox или unfinished webhook delivery. Опубликованные outbox
можно чистить раньше changefeed; consumer dedup TTL не меньше Kafka/replay/retry horizon.
Увеличение replay horizon требует сначала увеличить dedup TTL. Backfill старых событий вне окна —
отдельная операция с собственным idempotency планом.

## Миграции и проверки

goose migrations идут последовательно, без DB changes при импорте Go package. Применение —
отдельная команда/одноразовый Compose job; API стартует только на ожидаемой schema version.
Тесты применяют schema к чистой изолированной PostgreSQL, проверяют FK/unique, concurrent send/
DIRECT creation, dedup, rollback и проектные границы. Никакая миграция не очищает старую внешнюю БД.
