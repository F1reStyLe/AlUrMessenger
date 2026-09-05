# Реализованный WebSocket и recovery — Phase 4 и шаги 5.1–5.2

`GET /ws`, subprotocol `chat.v1`. TLS завершается на production ingress; публично только WSS.
Exact Origin allowlist общий с REST. Отсутствие Origin запрещено, кроме явного
`WS_ALLOW_NO_ORIGIN=true` для контролируемых небраузерных клиентов. Wildcard не используется.
JWT нельзя передать query/header/subprotocol: первая JSON text frame — auth, ≤8 KiB,
не позднее 5s. После неё frame ≤128 KiB. Все команды содержат request_id UUID и payload object.

```json
{"type":"auth","request_id":"01900000-0000-4000-8000-000000000001","payload":{"access_token":"<JWT>","device_id":"01900000-0000-4000-8000-000000000002"}}
```

Ответ auth.ok содержит connection_id, device_id, project_id, user_id, protocol_version,
heartbeat_interval_ms=25000 и token_expires_at. JWT хранится только в Session до disconnect;
Hub хранит routing/control handles. Auth подтверждает каждую команду и outbound frame,
а idle session проверяется каждую секунду. Отзыв/expiry закрывает 4401, сбой Auth — 1013.
При expiry ≤60s отправляется auth.expiring; клиент делает refresh в Auth и reconnect.
Новый Chat access/refresh token не создаётся. Bot API keys относятся к будущей фазе.

Authenticated envelope: type, request_id, conversation_id, payload.

| Команда | Payload | Результат |
| --- | --- | --- |
| conversation.subscribe | after_event_sequence, строка, default "0" | ack, reference events ASC, sync.complete |
| conversation.unsubscribe | {} | ack; membership не меняется |
| message.send | REST MessageSend: client_message_id, type TEXT, content, metadata?, reply_to_message_id? | ack с MessageSent после commit |
| message.edit | message_id, content, expected_version (строка) | ack с текущим Message; VERSION_CONFLICT при старой версии |
| message.delete | message_id | ack с tombstone; повтор не меняет version |
| reaction.add / reaction.remove | message_id, reaction | ack с текущим Message/reactions/version; повтор не создаёт событие |
| message.delivered / message.read | sequence, строка | ack с собственными checkpoint/version |
| typing.start / typing.stop | {} | ack, TTL 5s; CHANNEL moderator only |
| presence.watch | user_ids: UUID[], до 100 | ack со статусами; нужна подписка на conversation |

Forward — шаг 5.3, IMAGE — Phase 6. Pins меняются через REST и распространяются через WS events.
Reactions требуют allow_reactions, active membership и отсутствие bans; CHANNEL reader разрешён.
Edit/delete требуют автора, текущего права записи и allow_edit/allow_delete; reply — allow_reply.
Message ID обязан принадлежать conversation из envelope, проверка выполняется до mutation.
Ошибки: error payload {code,message}; никаких SQL/Auth response bodies или контента в логах.
Неизвестные поля, null top-level properties, trailing JSON и server-only envelope fields отклоняются.
Per-IP limit работает до upgrade; команды используют общую с REST Redis квоту Project/user.
Лимиты: 16 connections/user по Redis leases, 16 subscriptions/connection, inbound queue 16,
outbound queue 64, локальный Hub до 10000 connections. Медленный клиент получает 1013;
очереди не растут неограниченно, ошибки записи тоже закрывают socket.

Durable frames: type, event_id, conversation_id, event_sequence (строка), payload references;
message events дополнительно sequence. Для message.created/updated/deleted,
reaction.created/deleted, message.pinned/unpinned payload.message
содержит актуальный Message/tombstone, а не текст на момент исторического события.
Вложенный resource_version может быть новее reference resource_version. Клиент применяет
только актуальные версии и заменяет DTO целиком, удаляя отсутствующий content/metadata/reply.
Outbox/Kafka не содержат plaintext; hydration выполняется только после read authorization.
Перед выдачей уже поставленного в очередь frame membership и текущие privacy flags проверяются снова.
Message/relation events и send/edit/delete/reaction ack заново читают состояние перед выдачей.
Reactions и pin заменяются целиком по текущему resource_version; не применять исторические deltas.
После message.deleted/expired reactions=[] и pin отсутствует; клиент также убирает ID из pins.
Leave/kick → subscription.revoked без дальнейшего контента. Новый клиент получает snapshot;
повторно отправленная команда использует прежний client_message_id.

`GET /api/v1/conversations/{id}/snapshot` возвращает conversation, собственные checkpoints,
последние 50 доступных messages ASC (включая tombstones), все live pins (message IDs, в том числе
вне этих 50 messages), message_sequence и snapshot_event_sequence.
Это одна REPEATABLE READ transaction. Затем subscribe с этим event cursor.
`GET .../events?after_event_sequence=...&limit=50` — тот же PG replay, до 100 events.
Future/gapped cursor → 409 RESYNC_REQUIRED; WS resync.required содержит snapshot_url.
Клиент сохраняет последний применённый непрерывный cursor; duplicate event_id игнорирует.
В Phase 4 event log не очищается; retention/cleanup добавляется отдельно в Phase 10.

`POST .../read` и `POST .../delivered` с {"sequence":"42"} используют тот же service.
Checkpoint монотонный, не выше текущего message_sequence; READ также повышает delivered.
Это состояние пользователя, общее для устройств, не отчёт о записи байтов в socket.
Баны запрещают checkpoint/typing mutations. read_receipts=false сохраняет собственный
read checkpoint, но скрывает receipts других пользователей: replay выдаёт sync.advance,
сохраняя непрерывность event cursor.

Worker: outbox → Kafka → durable consumer inbox → Redis routing hint. API читает PG,
а не доверяет содержимому hint; periodic catch-up каждую секунду работает без hints.
Redis потерял сигнал — сообщение будет дочитано. Redis недоступен — команды fail closed,
presence/typing сообщают available=false; история/replay в открытом сокете ещё доступны,
но неудачный следующий heartbeat закрывает 1013. Reconnect требует восстановления Redis.

Heartbeat ping каждые 25s, ожидание pong до 10s; только успешный pong продлевает Redis lease
до 75s. Несколько устройств учитываются раздельно. Остановка одного не делает пользователя offline.
Worker раз в 30s переносит до 256 last-seen markers в PG через GREATEST и compare-before-remove.
Presence watch выдаёт presence.state только для текущих общих memberships; TTL и новые значения
проверяются каждые 2s. typing.state содержит current user_ids и expires_in_ms=5000;
оно заменяет клиентский список целиком. Эти state events выбраны вместо отдельных online/offline
и started/stopped дельт, чтобы потеря ephemeral frame не требовала восстановления истории.
Unavailable/disabled очищает UI cache, но не означает, что все offline.

Shutdown запрещает admission, отправляет server.draining, закрывает 1001 и дожидается
goroutines; через 5s принудительно закрывает оставшиеся sockets. HTTP shutdown сам по себе
не обслуживает hijacked WS, поэтому Hub drain подключён отдельно.
