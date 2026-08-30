# Identity: внешний Auth и локальные роли Chat

Обычный режим API — `AUTH_MODE=remote` (также при отсутствии AUTH_MODE). Login, refresh,
logout выполняются **в Auth**; его access JWT передаётся напрямую в Chat как Bearer.
Chat не выдаёт второй токен и не принимает refresh tokens. Поддерживается текущий Auth
с HS256, `sid`/`jti` и отзывом сессии: Chat не получает его секрет подписи.

На каждом защищённом запросе Chat вызывает `GET {AUTH_BASE_URL}/api/v1/users/me` с тем же
JWT. Auth проверяет подпись, expiration, состояние аккаунта и сессии; Chat использует
только положительный числовой `id` ответа как строковый `external_user_id`. Claims, роли,
имя и Project из Auth не дают прикладных прав. Положительного кеша нет: после успешного
logout Auth ранее выданные access JWT получают 401 и в Chat. Запрос, уже прошедший
проверку до logout, не отменяется задним числом.

Auth 401/403 → Chat 401; сеть/timeout/другой статус/некорректный ответ → Chat 503,
без автоматического выхода клиента и без обхода проверки. HTTP timeout 2s, bearer ≤8 KiB,
response ≤16 KiB; redirects и environment proxies отключены. Production требует HTTPS.
Readiness проверяет инфраструктуру Chat, а не доступность Auth; защищённые запросы
при его недоступности закрываются с 503.

`AUTH_BASE_URL` и `AUTH_PROJECT_ID` задаёт оператор до запуска. В текущем remote deployment
один экземпляр API обслуживает один настроенный Project; клиент не выбирает его header
или payload. Все записи/роли по-прежнему scoped по `(project_id, external_user_id)`.
Любой активный пользователь настроенного Auth может автоматически получить обычный
профиль в этом Project. Отдельный список допуска пользователей и маршрутизация нескольких
Auth/Projects в одном API пока не реализованы; нельзя считать эту привязку членством в Project.

Проект создаёт оператор с `MIGRATION_POSTGRES_URL[_FILE]`, после `migrate up`:

```powershell
go run ./cmd/project --id 75a13ce5-dd34-4029-9819-0b11e6e5172e --name Example --issuer https://auth.example.com --audience chat
```

Повтор с теми же параметрами безопасен; другие настройки существующего ID отклоняются.
Runtime не может создавать Project или менять его auth bindings. Право UPDATE(updated_at)
нужно для PostgreSQL row locks, но не позволяет перенаправить issuer/audience.
Поля issuer/audience сохранены для offline dev-rsa; remote использует конфигурацию endpoint,
а не эти поля. Не менять AUTH_BASE_URL на другой источник идентичностей при сохранённых
профилях Project: одинаковые числовые ID разных Auth могут обозначать разных людей.

## Назначение ролей

`chat.users.role` — источник истины: `user` по умолчанию или `admin`. Существующие профили
при migration 4 также получают `user`; автоматического повышения первого вошедшего,
пользователя с именем admin или глобального администратора Auth нет. `GET /api/v1/me`
возвращает `roles: ["user"]` либо `["admin"]` из БД Chat. Moderator относится к будущему
membership группы/канала и не является глобальной ролью.

Пользователь сначала входит в Auth и вызывает Chat `/api/v1/me`. Оператор берёт из ответа
точные `project_id` и `external_user_id` и выполняет с `MIGRATION_POSTGRES_URL[_FILE]`:

```powershell
go run ./cmd/user-role --project-id 00000000-0000-4000-8000-000000000001 --subject 42 --role admin
# В Compose (используйте -p alur-dev, если это имя вашего запущенного проекта):
docker compose run --rm --build user-role --project-id 00000000-0000-4000-8000-000000000001 --subject 42 --role admin
# Для понижения замените --role admin на --role user.
```

`42` — пример, не ID выбранного администратора. Команда не создаёт неизвестного пользователя,
не работает с неактивным Project и безопасно повторяется. Роль меняется на следующем запросе
с прежним JWT, без login/refresh. Policy mutation повторно проверяет роль и ban под lock;
после commit понижения устаревший Actor не разрешает запись. Runtime SQL не может
INSERT/UPDATE колонки role, HTTP профиль не принимает role/roles. В другой Project
назначение не переносится. Публичного API управления ролями пока нет; операторские CLI
действия требуют внешнего операционного журнала, текущий user audit фиксирует HTTP settings.

## Offline development fixture

Без Auth можно явно выбрать `AUTH_MODE=dev-rsa`, только при development/test; в production
этот режим запрещён. Он проверяет RS256, issuer/audience из Project, exp/sub/project_id,
token_use=access, nbf/iat при наличии, clock skew 30s. Roles из JWT игнорируются.
Отзыва внешней сессии здесь нет; это не fallback при недоступном Auth.

```powershell
$env:APP_ENV = 'development'
$env:AUTH_MODE = 'dev-rsa'
docker compose up -d --build --wait --wait-timeout 180
docker compose run --rm --build seed
$token = (go run ./cmd/dev-token --subject alice).Trim()
$adminToken = (go run ./cmd/dev-token --subject admin).Trim()
```

Dev Project: `00000000-0000-4000-8000-000000000001`, issuer `https://auth.local.dev`,
audience `alur-chat`. Seed создаёт профили admin/alice/bob без перезаписи существующих;
только новый fixture admin получает локальную роль admin. Повтор seed сохраняет ручное
понижение роли. Для уже существующего dev admin после migration 4 используйте явную
команду user-role с `--subject admin`, если ему нужны права. Token живёт 15 минут,
параметр dev-token `--admin` удалён: токен содержит идентичность, а права хранятся в БД.
Seed/dev-token отказывают при любом APP_ENV кроме development до доступа к БД/ключам.
Private key остаётся в `.local`, API получает только public key. Не печатать token в логи.

Реализовано: GET/PATCH `/api/v1/me`, GET `/api/v1/users`, GET `/api/v1/users/{user_id}`.
Первый валидный JWT атомарно создаёт пользователя с UUID-подобным display_name;
повтор/конкуренция сохраняют профиль. Внешняя идентичность уникальна внутри Project.
Все SQL запросы ограничены Project. Чужой UUID возвращает 404, public DTO скрывает
external_user_id, роль и ban metadata. PATCH принимает только display_name/avatar_url;
avatar URL — HTTPS без userinfo, не скачивается сервером. Banned пользователь read-only.
Presence пока не работает: status=offline, last_seen_at=null до соответствующей фазы.
Пагинация UUID keyset: limit=1..100 (default 50), opaque next_cursor или null.

Контракт с DTO/ошибками: [OpenAPI](../api/openapi/openapi.json). Swagger UI: `/docs/api` (API).
Unit security matrix и real PostgreSQL tests входят в `scripts/test-infrastructure.ps1`:
конкурентный provisioning, межпроектная изоляция, actor injection, profile persistence,
ошибки JWT без создания строк, локальные роли/понижение, SQL grants, Auth HTTP ошибки,
изоляция ролей между Projects и read-only ban. Production TLS deployment проверяется отдельно.
