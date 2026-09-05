# WebSocket protocol v1

Endpoint `/ws`, subprotocol `chat.v1`, production только WSS. Реализация Phase 4–5 и её
точные пределы описаны в [docs/REALTIME.md](docs/REALTIME.md); этот документ также содержит
целевые команды будущих фаз (bot credentials).
Текущие ephemeral события — presence.state/typing.state с полной заменой наблюдаемого
состояния вместо online/offline и started/stopped дельт. Presence watch ограничен 100 IDs.
Domain errors, DTO и flags совпадают с [REST](API_DESIGN.md); события — [EVENTS.md](EVENTS.md).

## Handshake и lifetime

1. Upgrade проверяет Origin allowlist и per-IP limit; отсутствие Origin разрешено только для
   конфигурируемых trusted non-browser clients, не как универсальный bypass.
2. До authentication единственная команда — `auth`, frame ≤ 8 KiB, deadline 5 секунд.
   JWT не передаётся в URL, subprotocol или логах.
3. JWT проверяется полным verifier; допускается bot credential с отдельным actor type.
   Session получает project_id, user_id, actor_type, permissions, connection_id и client device_id.
   Device ID не является credential и не даёт доступ к чужим connections.
4. Сервер отвечает `auth.ok` с connection_id, heartbeat interval, protocol version и token_expires_at.
5. После auth frame ≤ 128 KiB. 25-секундный heartbeat, Redis connection TTL 75 секунд;
   timeout/отсутствие pong закрывает соединение. Configurable bounds валидируются.
6. Перед истечением JWT — `auth.expiring`. По expiry сервер закрывает session с 4401;
   клиент получает новый JWT у внешнего Auth и reconnect. Refresh tokens сервер не принимает.

```json
{
  "type": "auth",
  "request_id": "01900000-0000-4000-8000-000000000010",
  "payload": {"access_token": "<JWT>", "device_id": "browser-installation-uuid"}
}
```

Секрет из auth frame не попадает в request logging. В queue/hub хранится проверенный Actor,
не raw JWT. Origin и identity проверяются независимо. Мутации снова проверяют текущие permissions
в application service: первоначальный JWT/session не фиксирует права навсегда.

## Envelope

```json
{
  "type": "message.created",
  "request_id": null,
  "event_id": "01900000-0000-4000-8000-000000000011",
  "conversation_id": "01900000-0000-4000-8000-000000000012",
  "sequence": "42",
  "event_sequence": "57",
  "payload": {"message_id": "01900000-0000-4000-8000-000000000013", "resource_version": "1"}
}
```

sequence — номер сообщения, nullable для не-message events. event_sequence — durable conversation
cursor; ephemeral events его не имеют. event_id — idempotency события, request_id коррелирует команду
и reply, client_message_id дедуплицирует отправку. Эти идентификаторы не взаимозаменяемы.
Все BIGINT поля JSON — строки. Для durable event сервер может приложить актуальный authorized
resource DTO; его resource_version может быть выше версии исходного события.

## Команды client → server

Каждая команда имеет request_id UUID. Успех — `ack` с request_id и result, ошибка — `error` с
code/message/details/request_id. Ошибка не создаёт успешного domain event.

| Type | Payload | Ack / права / idempotency |
| --- | --- | --- |
| auth | access_token или bot_api_key (ровно один), device_id | auth.ok; только до auth |
| conversation.subscribe | conversation_id, after_event_sequence? | ack + replay/sync.complete; active membership |
| conversation.unsubscribe | conversation_id | ack; удаляет локальную подписку, не membership |
| message.send | TEXT/reply, IMAGE с одним attachment_id или взаимоисключающий TEXT/IMAGE forward command из REST | ack.message содержит id/sequence/status=SENT после commit; client_message_id обязателен |
| message.delivered | sequence | ack checkpoint; max(old,new), membership, explicit client receipt |
| message.read | sequence | ack checkpoint, read_receipts; монотонный READ также обновляет delivered |
| message.edit | message_id, content, expected_version | ack; автор, allow_edit; version conflict защищает от потерянного update |
| message.delete | message_id | ack tombstone; автор, allow_delete; повтор не создаёт событие заново |
| reaction.add / reaction.remove | message_id, reaction | ack state/version; unique(user,message,reaction), allow_reactions |
| typing.start / typing.stop | conversation_id | ack; write permission, typing_enabled; TTL, не PostgreSQL |

Envelope conversation_id должен совпадать с resource/command. Команды без него допускаются только
для auth/global control. Reply/forward — варианты message.send по REST contract. Pin, creation,
membership, moderation и upload в MVP выполняются REST; результат распространяется через WS events.
Подписка не требуется для отправки, но membership и channel permission обязательны всегда.

## События server → client

| Type | Полезная нагрузка | Sequence / обработка |
| --- | --- | --- |
| message.created / updated / deleted | reference + актуальный Message/tombstone | message sequence и event sequence; version-aware upsert |
| reaction.created / deleted | message_id, message_sequence, resource_version + current payload.message | durable event sequence; полная замена текущего reactions state |
| message.pinned / unpinned | message_id, message_sequence, resource_version + current payload.message | durable event sequence; pin collection по current message.pin/status |
| message.delivered / read | user_id, checkpoint, member_version | durable event sequence, без content; монотонный max |
| conversation.created / updated | Conversation DTO/reference | durable conversation cursor |
| member.joined / left / updated | user_id, role/status, member_version | durable; событие удаления не даёт доступ к последующему контенту |
| user.online / offline | user_id, observed_at, last_seen_at | ephemeral; только общим разрешённым conversations |
| typing.started / stopped | user_id, expires_at | ephemeral; stale state удаляется по TTL |
| sync.complete | through_event_sequence, has_more=false | Barrier catch-up завершён, не значит отсутствия новых событий |
| sync.advance | through_event_sequence | Продвинуть cursor через скрытые текущей privacy/flag policy события без раскрытия payload |
| resync.required | min_available_event_sequence, snapshot_url | Старый/некорректный recovery cursor; content не раскрывается |
| subscription.revoked | conversation_id, reason_code | Удаляет подписку и очищает недоступный cache клиента |
| auth.expiring / server.draining | expires_at или retry_after_ms | Control, не domain event |

Ephemeral presence/typing не восстанавливаются из истории. Последнее состояние presence запрашивается
заново; typing исчезает по TTL. Свой read status синхронизируется на всех devices даже без других
recipients online. Получение сообщения другой вкладкой не означает READ.

## Надёжность и recovery

### Обычный reconnect

Клиент хранит последний **применённый непрерывный** event_sequence каждого conversation и IDs
неподтверждённых отправок. После authentication подписывается с after_event_sequence.
Сервер начинает buffering live hints, проверяет membership и читает DB high-watermark E.
Затем отдаёт events `(cursor, E]` по возрастанию, страницами, завершает `sync.complete(E)`
и продолжает с E+1. Buffered hints лишь будят чтение из changefeed; порядок задаёт БД.

При выключенных read_receipts/presence сервер не раскрывает старые сигналы других users через
replay/snapshot. Скрытые durable события заменяются sync.advance, сохраняя непрерывность cursor;
нельзя просто выкинуть номер и заставлять клиента бесконечно искать gap.

При пропуске номера клиент/сервер дочитывает changefeed, не считает курсор равным максимальному
увиденному номеру. Duplicates event_id/event_sequence игнорируются, старая resource_version
не перезаписывает новую. WS retransmit не создаёт новое сообщение: resend использует тот же
client_message_id. Для старого клиента один last_known_sequence может дочитать новые messages,
но полноценная синхронизация всегда использует event cursor.

### Первая подписка или просроченный cursor

Клиент получает snapshot REST из одной REPEATABLE READ transaction: состояние conversation,
membership/checkpoints, pins, recent history и snapshot_event_sequence. Старый cache этого
conversation инвалидируется, подписка продолжается с snapshot cursor; события после snapshot
дочитываются из changefeed. Более старая history грузится отдельно с message cursor.
Нельзя использовать независимые GET history/GET counters как согласованный snapshot.

Если retention удалил промежуточные события, `RESYNC_REQUIRED` вместо молчаливой потери.
При hydration event сервер выдаёт только **текущее** доступное состояние: уже удалённое сообщение
не воскресает из старого message.created event. Это state synchronization, не выдача истории revisions.

### Потеря Redis / медленный клиент

Redis hints не являются подтверждением доставки. API периодически сверяет durable cursor с PG
даже без reconnect; поэтому потерянный Pub/Sub hint не оставляет открытую session навсегда устаревшей.
Bounded send queue: при переполнении сервер закрывает socket с 1013, клиент восстанавливается из PG.
Безлимитные очереди и silent drop сообщений запрещены.

## Privacy, bans и shutdown

После leave/kick Project/resource permissions перепроверяются перед hydration/delivery. Удалённый
участник получает control revoke без дальнейшего content; его cursor не даёт обхода membership.
Project ban сохраняет read-only, но запрещает domain write commands, включая read/delivered
checkpoint и typing. Protocol pong/auth/reconnect и выдача download capability остаются допустимыми
операциями чтения/поддержания соединения, не дают возможности изменить доменную модель.
С шага 7.3 уже открытая session не доверяет cached Actor для write: application transaction читает
global/conversation ban из PostgreSQL. Поэтому ban действует на следующую command, соединение
остаётся read-only, а unban восстанавливает запись без JWT refresh/reconnect.

Shutdown прекращает upgrades/commands, отправляет server.draining, даёт bounded срок текущим
транзакциям и закрывает sockets 1001. После неопределённого исхода команда повторяется с тем же ID.
Client map/queues имеют одного владельца закрытия; tests покрывают race, slow consumer и drain.
