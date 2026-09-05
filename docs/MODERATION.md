# Moderation — реализованный шаг 7.1

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
немедленно разрешает новые writes; сохранённые messages остаются доступны. Ban/report/moderator
delete реализуются следующими подшагами Phase 7.
