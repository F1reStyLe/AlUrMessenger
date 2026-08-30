# Conversations — Phase 2

Шаг 2.1 реализует POST/GET `/api/v1/conversations`, GET/PATCH
`/api/v1/conversations/{id}`. DIRECT — ровно два разных пользователя одного Project;
повторный или обратный запрос возвращает 200 того же диалога, новый — 201 с Location.
GROUP/CHANNEL приватные; создатель автоматически moderator, приглашённые — member.
Пустая группа/канал с одним создателем допустимы. Начальный список — максимум 100 других
существующих пользователей; Project/actor не задаются в payload. Bot требует allow_bots.

Пример: `{"type":"DIRECT","member_ids":["UUID-другого-пользователя"]}`.
Метаданные GROUP/CHANNEL: title до 128 символов без крайних пробелов/control chars,
avatar_url до 2048 bytes, HTTPS без userinfo. Пустая строка очищает значение;
URL сервер не скачивает. PATCH требует expected_version строкой и хотя бы одно поле;
DIRECT immutable (422), stale version 409, не-moderator 403. Project admin не обходит
membership или moderator. Чужой Project/отсутствие membership скрываются как 404.

Список содержит только активные own memberships. Ascending UUID v7 cursor не меняется
при редактировании title; limit=1..100, default 50. Cursor привязан к Project/user,
не является credential: SQL всегда проверяет scope и membership. GROUP/CHANNEL не
являются публичными и не появляются в списке посторонних пользователей.

Schema 5: conversations, direct_pairs, conversation_members, composite tenant FK.
Canonical pair PK и отдельный transaction advisory lock сериализуют только одну пару.
DB trigger запрещает третьего DIRECT участника/смену его роли/leave; deferred constraint
не позволяет commit неполного DIRECT или GROUP/CHANNEL без creator moderator.
Runtime не меняет тип/Project/creator, не удаляет conversation/pair/membership.
Creation и metadata patch записывают audit в своей транзакции без title/URL в metadata.

DELETE/архивация не добавлены: их нет в текущем контракте. Messages/counters/outbox/WS
появятся в следующих фазах. Управление membership относится к 2.2. Контракт запросов,
ответов, ошибок и CORS: [OpenAPI](../api/openapi/openapi.json).

## Membership (2.2)

GET/POST `/api/v1/conversations/{id}/members`, PATCH/DELETE
`/api/v1/conversations/{id}/members/{user_id}`. Create/add используют уже существующие
Project users, batch максимум 100. Moderator GROUP/CHANNEL приглашает/исключает,
назначает role member/moderator и ban; human moderator не является Project admin.
Обычный участник может выйти или изменить только собственный muted. DIRECT поддерживает
self-mute, но не add/leave/role/ban. Bot нельзя назначить moderator: текущий management API
работает с human actors. Отключение allow_bots запрещает новые приглашения, не блокирует
удаление уже существующего bot из conversation.

PATCH требует expected_version и хотя бы одно поле, stale version -> 409. Последнего
активного неблокированного moderator нельзя понизить, исключить или заблокировать:
LAST_MODERATOR_REQUIRED (409). Conversation row lock сериализует конкурирующие изменения.
Leave ставит left_at, не удаляет историю; доступ последующих запросов становится 404.
Повтор add сохраняет active role/ban/mute; rejoin назначает member, очищает ban/left_at,
повышает version и сохраняет первоначальный joined_at/mute. Banned member сохраняет
чтение, но не запись. Все изменения сопровождаются atomic audit без содержимого сообщений.

Seed создаёт стабильные dev DIRECT alice/bob, GROUP и CHANNEL (creator admin moderator).
Повтор не восстанавливает удалённые membership/изменённые роли и не перезаписывает title.
Наличие dev moderator не выдаёт права администратора Project.
