# Moderation — реализованные шаги 7.1–7.2

Project admin управляет whole-word blacklist через `GET/POST /admin/v1/blacklist` и
`PATCH/DELETE /admin/v1/blacklist/{id}`. Client не передаёт Project/actor. Word обязан быть ровно
одним Unicode letter/digit token; сервер применяет NFC и Unicode case folding. UNIQUE внутри Project
не позволяет завести дубликат другим регистром. PATCH меняет `enabled` с `expected_version`.

`PATCH /admin/v1/project` включает `blacklist_enabled`; `blacklist_policy` в MVP принимает только
`reject`. `/admin/v1/feature-flags` эти поля не принимает. Create/update/delete фиксируются вместе
с append-only audit; metadata содержит только enabled/changed field, но не запрещённое слово.

При включённой policy единый PostgreSQL transaction gate проверяет TEXT, IMAGE caption, edit,
forward snapshot и общий human/bot/internal SYSTEM send path. Сопоставление целых слов не является
substring: `спам` блокирует `СПАМ!`, но не `спамер`. Rejection возвращает 422 `CONTENT_REJECTED`
без совпавшего слова и до message/sequence/search/outbox mutation. Exact idempotent retry уже
зафиксированного сообщения возвращает receipt без повторной content write.

Blacklist не сканирует и не удаляет старую историю. Отключение Project policy или отдельной entry
немедленно разрешает новые writes; сохранённые messages остаются доступны. Ban/moderator delete
реализуются следующими подшагами Phase 7.

Жалобу на доступное live сообщение создаёт незаблокированный human через
`POST /api/v1/messages/{id}/reports`. Жалоба на user того же Project создаётся через
`POST /api/v1/users/{id}/reports`; optional `conversation_id` принимается только когда reporter и
target остаются участниками. Self/system target, bot/system reporter и чужой Project отвергаются.

Описание до INSERT шифруется отдельным purpose/AAD; БД, audit и события не содержат plaintext.
Reporter читает свой безопасный DTO через `GET /api/v1/reports/{id}`. Project admin получает очередь
`GET /admin/v1/reports` с status/cursor/limit и reviewer DTO по item GET. PATCH item допускает только
`OPEN → REVIEWING → RESOLVED|REJECTED`, требует `expected_version`; terminal state неизменяем.
Каждый успешный review transition записывается в audit без description. Banned admin может читать,
но не менять очередь. Kafka report events будут подключены к generic aggregate outbox при 8.5.
