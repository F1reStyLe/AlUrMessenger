# Messages — реализованный контракт Phase 3

REST: POST/GET `/api/v1/conversations/{id}/messages`, GET `/api/v1/messages/{id}`,
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
Если контент истёк раньше receipt, повтор получает 404 и не воскрешает сообщение.

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
Каждое чтение сначала проверяет действующее membership; expired rows исключены SQL.
Физическая retention cleanup — Phase 10, логическое истечение работает сейчас.

Outbox публикует chat.events.v1 с Kafka key=conversation_id и ack=all. Worker держит
aggregate advisory lock и публикует только первый pending event; ошибка блокирует
этот conversation с backoff до 60s, другие продолжаются. Ack перед DB commit может
дать повтор того же event_id. Нельзя обещать exactly-once или считать Pub/Sub доставкой.
Event JSON содержит только ссылки и имена изменённых полей, без content/metadata/JWT.
Topic создаётся операторским Compose job, auto-create в брокере выключен.
