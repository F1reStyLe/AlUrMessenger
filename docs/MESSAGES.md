# Messages — реализованный контракт Phase 3 и шага 5.1

REST: POST/GET `/api/v1/conversations/{id}/messages`, GET/PATCH/DELETE `/api/v1/messages/{id}`,
GET `/api/v1/conversations/{id}/search`. Точные схемы и ошибки — в OpenAPI.
Публичная отправка TEXT, внутренний SendSystem требует реальную identity kind=system.
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
удалять message. Реакции, pins и forward остаются шагами 5.2–5.3; IMAGE — Phase 6.
