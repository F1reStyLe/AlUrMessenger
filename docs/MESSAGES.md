# Messages — реализованный контракт Phase 3 и Phase 5

REST: POST/GET `/api/v1/conversations/{id}/messages`, GET/PATCH/DELETE `/api/v1/messages/{id}`,
GET `/api/v1/conversations/{id}/search`. Точные схемы и ошибки — в OpenAPI.
Публичная отправка TEXT/IMAGE, внутренний SendSystem требует реальную identity kind=system.
При включённом Project blacklist новый content во всех этих путях проверяется до persistence;
совпадение возвращает CONTENT_REJECTED без sequence/event (подробно в MODERATION.md).
IMAGE с attachment_id предусмотрен Phase 6; сейчас IMAGE отклоняется, загрузки ещё нет.
GROUP/DIRECT: active member; CHANNEL: moderator. Баны запрещают запись, сохраняют чтение.
После leave нет доступа ни к истории, ни к поиску. Project admin membership не обходит.

Сообщение, message_sequence, last_message_id, blind index, receipt, event_sequence,
event log и outbox записываются одной транзакцией. SENT возвращается только после commit.
Счётчики сериализуются строкой conversation; rollback откатывает выделенный номер.
client_message_id scoped по Project/sender на 365 дней. Повтор возвращает прежний ID;
иной payload/conversation даёт 409. Fingerprint — keyed HMAC канонического JSON.
Окончание срока возвращается явно. После срока ID может создать новое сообщение.
Если сохранённая запись удалена или истекла раньше receipt, повтор получает 200 с тем же
ID и актуальным tombstone без content/metadata/reply. Внешний статус SENT подтверждает
исходный commit; текущее состояние описывает вложенный message.status.

Content и metadata зашифрованы AES-256-GCM, новый случайный nonce для каждой записи.
AAD связывает Project, conversation, message, payload version и key version.
CONTENT_KEYS_FILE содержит независимые encryption/search/fingerprint keyrings;
HKDF дополнительно разделяет Projects и назначения. Старые версии сохраняют чтение,
поиск и проверку повторов. Для ротации сначала добавить ключи всем API, затем сменить
active версии; удалять старые только после миграции/истечения всех связанных данных.
Потеря ключей означает потерю доступа к данным: нужен отдельный защищённый backup.
dev-init создаёт `.local/content-keys.json`, существующий секрет не заменяет.

Поиск: Unicode NFC + case folding, слова из букв/цифр, AND до 8 слов. Индекс до 2048
различных слов хранит HMAC, plaintext-копий нет. Это не E2EE и не полнотекстовый ranking:
видимы routing headers, размеры и равенство/частотность токенов внутри Project.
Каждое чтение сначала проверяет действующее membership. Поиск исключает deleted/expired rows.
GET, history и snapshot сохраняют их id/sequence/version/status без прежнего content,
metadata или reply. Tombstone читается даже без старого ключа шифрования.
Физическая retention cleanup — Phase 10, логическое истечение работает сейчас.

Outbox публикует chat.events.v1 с Kafka key=conversation_id и ack=all. Worker держит
aggregate advisory lock и публикует только первый pending event; ошибка блокирует
этот conversation с backoff до 60s, другие продолжаются. Ack перед DB commit может
дать повтор того же event_id. Нельзя обещать exactly-once или считать Pub/Sub доставкой.
Event JSON содержит только ссылки и имена изменённых полей, без content/metadata/JWT.
Topic создаётся операторским Compose job, auto-create в брокере выключен.

## Ответы, редактирование и удаление — 5.1

Reply: необязательный `reply_to_message_id` в MessageSend (если не нужен, поле опустить,
null не принимается). Нужны allow_reply и живой источник из того же conversation/Project;
чужой, удалённый или истёкший источник даёт 404. Composite FK дублирует эту границу в БД.
Response содержит `reply: {message_id}`; текста/metadata источника внутри ответа нет.
Preview клиент получает через авторизованный GET источника и заменяет по его версии.
Удаление источника не удаляет самостоятельное сообщение-ответ, но GET источника выдаёт tombstone.
Reply target входит в send fingerprint; добавление поля не изменило fingerprints старых команд.
Matching retry проверяет текущий allow_reply, но не требует, чтобы источник всё ещё был живым.

PATCH принимает `{"content":{"text":"Новый текст"},"expected_version":"1"}`.
Только автор TEXT с действующим правом записи и allow_edit; SYSTEM и чужие сообщения — 403,
включая попытку moderator/Project admin. Удалённое/истёкшее сообщение — 404.
Несовпадение resource_version — 409 VERSION_CONFLICT. Даже равный текст при верной версии
повышает version и edited_at; metadata/reply/sequence/created_at/expires_at неизменны.
Новый ciphertext с новым nonce, полный blind index и message.updated фиксируются атомарно.
Версии старого текста не хранятся. Повтор исходного send возвращает актуальный текст/версию.

DELETE требует тех же авторских прав и allow_delete, без optimistic precondition.
Первый вызов очищает ciphertext и search index, устанавливает deleted_at, повышает version,
пересчитывает last_message_id по последнему живому сообщению и создаёт message.deleted.
Повтор возвращает тот же tombstone без повышения version/нового события; текущие bans/flags
проверяются и на повторе. Можно удалить и истёкшую запись; физическое удаление — Phase 10.
Остальные ответы сохраняют только безопасную ссылку. Административное удаление — будущая Phase 7.

В WS доступны message.edit и message.delete с message_id в payload. Conversation в envelope
обязан соответствовать сообщению; несовпадение отклоняется **до** изменения БД.
Event sequence растёт на edit/delete, message sequence остаётся прежним.
REST/WS replay добавляет в payload.message текущий Message/tombstone даже для старого
message.created; stored event/outbox/Kafka остаются reference-only. Поставленные в очередь
WS events и send/edit/delete acknowledgements обновляются непосредственно перед выдачей.
Клиент применяет вложенный resource_version и заменяет DTO целиком: нельзя сохранять старый
content, когда он отсутствует в tombstone. Event cursor и resource_version — разные счётчики.

Миграция 9 добавляет reply reference, edited_at и только необходимые UPDATE grants.
Runtime по-прежнему не может менять sender/sequence/TTL, переписывать event log или физически
удалять message. TEXT/IMAGE forward описан ниже и использует независимый encrypted snapshot.

## Реакции и закрепления — 5.2

`PUT/DELETE /api/v1/messages/{id}/reactions/{reaction}` изменяет только реакцию actor.
В URL передаётся один percent-encoded emoji из Unicode Emoji 16.0; например `❤` и `❤️`
обозначают одну canonical reaction `❤️`. Family/ZWJ, flags, keycaps, skin tones поддержаны.
Произвольный текст/несколько emoji/неполная последовательность дают 400 INVALID_REQUEST.
Нужно active membership, allow_reactions, отсутствие Project/membership ban; CHANNEL reader
также может реагировать. Bot дополнительно требует allow_bots. Тело запроса не используется.
Ответ 200 — полный текущий Message, `reactions:[{reaction,count}]`, count как decimal string.
До 32 разных emoji на сообщение; новые users могут выбрать уже существующий emoji при лимите.
Превышение — 400. Список users не возвращается; counts — агрегированное состояние.

`GET /api/v1/conversations/{id}/pins` возвращает `{items:[Pin],snapshot_event_sequence}`
в одном snapshot. Pin содержит message_id, pinned_by, created_at, current resource_version;
содержимое читается через GET message. Порядок по message sequence, до 100 live pins.
`PUT .../pins/{message_id}` → 200 Pin; `DELETE .../pins/{message_id}` → 204.
Изменять pins может только moderator GROUP/CHANNEL с allow_pin; Project admin без moderator
не имеет обхода, DIRECT pins запрещены (403). Источник обязан быть в том же conversation.
Flags блокируют writes, но не скрывают existing relations при чтении.

Успешное изменение relation повышает message.resource_version и event cursor; sequence,
edited_at и content неизменны. Повторы не меняют version/event и не присваивают заново pinned_by.
Поэтому edit с прежним expected_version конфликтует также после reaction/pin.
Reaction/pin event и association фиксируются одной transaction; ошибки откатывают оба.

GET/history/search/snapshot/replay возвращают current reactions/pin. Message имеет optional
`pin:Pin` и обязательный массив reactions (пустой у tombstone). Soft delete очищает associations
атомарно; deleted/expired read никогда не выдаёт relations, даже через старый created/pinned event.
Новый add к terminal message даёт 404; remove — no-op после всех permission/flag checks.
Snapshot.pins содержит все live message IDs, даже вне 50 recent messages. При replay клиент
заменяет Message целиком и добавляет/удаляет ID в pin collection по текущему message.pin/status,
а не по историческому имени события. GET pins даёт watermark для согласования полной коллекции.

## Пересылка TEXT/IMAGE — 5.3/6.2

Тот же REST POST и WS `message.send` принимают отдельную форму
`{client_message_id,forwarded_from_message_id}`. Она не смешивается с type/content/metadata/reply,
включая явно пустые поля. Нужны allow_forward, read access к живому source и write access к target;
оба conversation/message обязаны принадлежать Actor Project. Недоступный, удалённый или истёкший
источник скрывается как 404. SYSTEM не пересылается, IMAGE будет реализован вместе с attachments.

Target получает новый UUID/sequence/sender/TTL, новый AES-GCM nonce/AAD и собственный blind index.
Encrypted payload содержит копию TEXT либо IMAGE caption и `{message_id,original_sender:{id,display_name}}`.
При повторной пересылке сохраняется root original_sender, а message_id указывает на непосредственный
source. Source conversation не выдаётся. Metadata, reply, reactions и pin не копируются.
Изменение или удаление source не меняет forward; physical purge очищает только nullable FK header,
а authenticated snapshot остаётся читаемым. Soft delete самого forward удаляет и его snapshot.

Source ID и target conversation входят в HMAC fingerprint. Matching retry возвращает текущий target
message, повторно проверяя target permissions и allow_forward, но уже не зависит от source lifecycle.
Stored `message.created` остаётся reference-only и содержит лишь `forwarded:true`; body и attribution
появляются только при authorized hydration. Cross-conversation forward transaction сериализуется
per-Project advisory lock, чтобы встречные пересылки не образовали lock-order deadlock.

## Итоговая матрица Phase 5 — 5.4

| Операция | Обязательный flag | Проверяется на повторе/no-op | Current state |
| --- | --- | --- | --- |
| send reply | allow_reply | да, matching receipt | reply reference в Get/history/search/snapshot/replay |
| edit | allow_edit | каждый optimistic request | новый body/version во всех read paths |
| delete | allow_delete | да, повтор terminal delete | tombstone без body/reply/forward/relations |
| reaction add/remove | allow_reactions | да, existing add/absent remove | агрегированные counts либо пустой terminal state |
| pin/unpin | allow_pin | да, existing pin/absent remove | Message.pin и полная snapshot.pins collection |
| send forward | allow_forward | да, matching receipt | независимый encrypted snapshot либо redacted tombstone |

Flags блокируют новые mutations, но не скрывают уже разрешённое сохранённое состояние.
Каждый message-affecting event хранит только reference. Authorized REST replay и WS delivery
гидратируют один полный текущий Message; клиент заменяет DTO целиком. Интеграционный audit
проводит forward через created/updated/reaction.created/reaction.deleted/pinned/unpinned/deleted
и требует одинаковый финальный tombstone во всех событиях, history и snapshot.
