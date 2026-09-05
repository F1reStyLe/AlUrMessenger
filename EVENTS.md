# Kafka, changefeed и webhooks

События — versioned факты; source of truth — PostgreSQL. Phase 3–5 реализуют
conversation/message/checkpoint events и consumer inbox; остальные события ниже — будущие фазы.
Machine-readable [AsyncAPI](api/asyncapi/asyncapi.json) описывает действующий Kafka transport.
HTTP-публикация `/docs/asyncapi` и `/docs/webhooks` будет добавлена с документацией интеграций.
Текущие membership event types: conversation.members.added, conversation.member.updated,
conversation.member.left. Шаг 5.1 добавляет message.updated и message.deleted. Outbox не включает message content.
REST/WS replay дополняет message.created/updated/deleted полем payload.message с текущим
авторизованным Message/tombstone. Историческая reference version сохраняется, а клиент
применяет вложенный current resource_version: старый created не восстанавливает удалённый текст.
Шаг 5.2 также добавляет reference-only reaction.created/deleted, message.pinned/unpinned
с message_id/message_sequence/resource_version. Authorized replay гидратирует для них целый
current Message с reactions/pin; consumer не применяет исторические relation deltas (D37).
TEXT/IMAGE forward использует существующий reference-only `message.created`; payload содержит только
`forwarded:true`, id/type/version/sequence и sender target-сообщения, без текста, snapshot автора
или идентификатора source conversation. Authorized hydration возвращает текущий forward DTO.

## Envelope v1

```json
{
  "event_id": "01900000-0000-4000-8000-000000000020",
  "event_type": "message.created",
  "event_version": 1,
  "project_id": "01900000-0000-4000-8000-000000000021",
  "aggregate_type": "conversation",
  "aggregate_id": "01900000-0000-4000-8000-000000000022",
  "aggregate_sequence": "57",
  "occurred_at": "2026-08-30T10:00:00Z",
  "payload": {
    "message_id": "01900000-0000-4000-8000-000000000023",
    "message_sequence": "42",
    "resource_version": "1"
  }
}
```

event_id генерируется один раз до DB commit и не меняется при retry. event_version относится
к contract schema, resource_version — к состоянию сущности, aggregate_sequence — к порядку её событий.
Kafka/outbox/webhook payload не содержат plaintext/ciphertext message content, JWT, API keys,
webhook secrets, signed download URLs. Привилегированный consumer получает content через
авторизованный application query с повторной проверкой flags/membership/bans.

## Topics и ordering

Начальный durable topic — `chat.events.v1`; key = aggregate_id, для message events это
conversation_id. UUID глобально уникальны, project_id всё равно проверяется consumer.
Один topic сохраняет порядок conversation changes без попытки собрать общий порядок разных topics.
Новые topics вводятся по потребности throughput/retention/ACL, не по одному на каждую функцию.

Kafka producer: acknowledgments all, retries bounded по времени операции, idempotent producer
при поддержке выбранного client. Это не distributed exactly-once. Production replication/min ISR
подбираются при deployment; single-broker Compose не имеет отказоустойчивости production кластера.
Kafka retention default 7 дней; PG history/changefeed независимы. Replay после Kafka retention —
явный DB backfill, а не обещание бесконечного Kafka log.

Outbox publisher сериализует aggregate PostgreSQL advisory transaction lock. Только head pending
event может публиковаться; его next_attempt_at блокирует следующие события этого aggregate.
Publish ack → mark published → commit. Crash после ack до commit вызывает duplicate event_id.
Время блокировки ограничено producer deadline, batch малый. Poison event не выбрасывается молча:
aggregate остаётся pending, ошибка видна в structured logs/операторском статусе, другие aggregates идут.

## Каталог

| event_type | Aggregate | Payload references / смысл |
| --- | --- | --- |
| message.created | conversation | message_id, message_sequence, sender_id, resource_version, type |
| message.updated / deleted | conversation | message_id, message_sequence, resource_version |
| reaction.created / deleted | conversation | message_id, user_id, reaction, message_version |
| message.pinned / unpinned | conversation | message_id, pinned_by, message_version |
| message.delivered / read | conversation | user_id, checkpoint, member_version |
| conversation.created / updated | conversation | conversation_id, type, resource_version |
| member.joined / left / updated | conversation | user_id, role/status, member_version |
| report.created / updated | report | report_id, target references, status; без description/content |
| user.banned / unbanned | user | user_id, policy_version; reason только privileged DTO |
| project.settings.updated | project | settings_version, changed flag names |
| user.online / offline | user | observed_at, last_seen_at; observation, не durable presence truth |

REST workflow reports реализован в 7.2, но текущий outbox физически conversation-scoped. Поэтому
report rows и review audit уже durable, а Kafka `report.*` не имитируется частичной публикацией:
generic aggregate outbox/event_streams вводится и проверяется вместе с contract freeze 8.5.

Последние два presence события экспортируются только при включённых presence и соответствующих
подписках. Redis edge observer создаёт outbox record; запись в Redis и PostgreSQL не является
одной транзакцией. Presence observations могут быть пропущены/устареть: не обещать полный журнал
online/offline. Они не входят в conversation replay. Heartbeats/typing в PostgreSQL не пишутся.
Если Redis недоступен, presence DTO возвращает availability=unavailable, status=null и отдельно
последний сохранённый last_seen_at, а не выдуманный offline. Domain status остаётся online/offline;
клиент не показывает last_seen_at как время текущего разрыва связи.

## Consumers

- `realtime-router`: Kafka → Redis hints. Дубликаты допустимы; API по durable cursor дочитывает PG.
  Если marker/offset продвинулся, а hint потерян, periodic catch-up всё равно восстанавливает delivery.
- `webhook-scheduler`: event → уникальные webhook_deliveries; insert deliveries и consumer marker
  в одной PostgreSQL transaction, Kafka offset подтверждается после commit.
- `echo-bot`: только message.created человеческого автора в разрешённом conversation,
  bot enabled + allow_bots. Игнорирует SYSTEM/bot messages, собственные события и disabled hooks.
  Отправляет с детерминированным UUID client_message_id из `(bot_id, source_event_id)`.
  При crash между send и consumer marker повтор dedup send не создаёт второй ответ.

`consumer_deduplication` scoped project/consumer/event_id. Не ставить marker до local side effect
и не считать один marker гарантией exactly-once внешнего HTTP. External recipients дедуплицируют сами.
При unsupported event_version consumer не подтверждает молча потерю нужного события; хранит error/
retry state и сообщает оператору. Неизвестный необязательный event_type может быть явно пропущен
по документированной subscription policy.

## Webhook protocol

Admin регистрирует HTTPS endpoint, список event types и enabled. Создание/rotation возвращает
secret один раз; в БД encrypted secret, доступный только worker. allow_webhooks=false запрещает
новые доставки и ставит pending deliveries на паузу; при включении учитываются их expiry/retry limits.
Удаление/disable subscription прекращает дальнейшие попытки, но не отменяет уже отправленный HTTP.

POST body — exact JSON bytes envelope. Headers:

```text
Content-Type: application/json
X-Chat-Event-ID: <event_id>
X-Chat-Timestamp: <unix seconds of this attempt>
X-Chat-Signature: v1=<hex HMAC-SHA256(secret, timestamp + "." + raw_body)>
```

Получатель проверяет signature constant-time по **raw bytes**, timestamp с допустимым skew
(начально 5 минут), затем дедуплицирует event_id; повтор успешного события отвечает 2xx.
Timestamp/signature новые для каждой попытки, event_id/body неизменны. Replay operator retry
сохраняет event_id. TLS transport и SSRF policy описаны в [SECURITY.md](SECURITY.md).

Начальная политика: timeout 5 секунд, exponential backoff от 5 секунд до 1 часа с jitter,
maximum 12 attempts и 24 часа возраста delivery. 2xx → delivered; network/408/425/429/5xx → retry;
остальные 4xx → failed. Retry-After учитывается в пределах cap/age. Redirects запрещены.
По исчерпанию лимита delivery сохраняется failed; admin может повторить после исправления причины.
Попытки записывают status/error code/duration без response body и секретов.

Semantic ordering webhook arrivals не гарантируется: retries могут доставить более старое событие
после нового. Получатель применяет aggregate_sequence/resource_version либо читает актуальное
состояние. При secret rotation pending retry подписывается active secret, администратор обязан
сначала подготовить получателя; dual-secret grace period — отдельное будущее расширение.

## Версионирование и retention

Envelope v1 допускает добавление необязательных полей. Удаление/изменение semantics обязательного
поля требует event_version+1, transitional consumers и документированного rollout.
Topic suffix — крупная транспортная версия; event_version конкретного типа эволюционирует отдельно.

Published outbox retention default 7 дней; unpublished не удалять. Changefeed и message dedup
живут не меньше согласованного project retention/recovery периода (default 365 дней), consumer
dedup — не меньше Kafka/replay/webhook retry horizon. Увеличение replay window сначала требует
увеличить dedup retention. Notification Service позже подписывается на message.created и использует
scoped lookup; notification logic не добавляется в Chat API.
