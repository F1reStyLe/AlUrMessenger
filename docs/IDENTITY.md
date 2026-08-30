# Identity Foundation 1.4

API проверяет внешний access JWT RS256 с public PEM из `AUTH_PUBLIC_KEY_FILE`.
Обязательны `iss`, `aud`, `exp`, непустой `sub`, UUID `project_id`, `token_use=access`
и массив `roles` (`user`/`admin`). Проверяются подпись, алгоритм, expiration, nbf/iat при
наличии; clock skew 30 секунд. Refresh token и глобальный moderator не принимаются.
Issuer/audience берутся из заранее созданного Project, а не из доверия к payload JWT.
Public key общий для настроенного внешнего Auth issuer infrastructure: этот Auth должен
иметь право выдавать claims для всех обслуживаемых Projects. Независимые недоверяющие
друг другу issuers требуют отдельного key binding/JWKS extension до подключения.
Произвольные `jku`/`x5u`/`kid` не вызывают сетевых запросов. Rotation требует restart.

Проект создаёт оператор с `MIGRATION_POSTGRES_URL[_FILE]`, после `migrate up`:

```powershell
go run ./cmd/project --id 75a13ce5-dd34-4029-9819-0b11e6e5172e --name Example --issuer https://auth.example.com --audience chat
```

Повтор с теми же параметрами безопасен; другие настройки существующего ID отклоняются.
Runtime не может создавать Project или менять его auth bindings. Право UPDATE(updated_at)
нужно для PostgreSQL row locks, но не позволяет перенаправить issuer/audience.

Для локального Compose после `dev-init` и `up -d --build`:

```powershell
docker compose -p alur-dev run --rm --build seed
$env:APP_ENV = 'development'
$token = (go run ./cmd/dev-token --subject alice).Trim()
Invoke-RestMethod http://127.0.0.1:8080/api/v1/me -Headers @{Authorization="Bearer $token"}
$adminToken = (go run ./cmd/dev-token --subject admin --admin).Trim()
```

Dev Project: `00000000-0000-4000-8000-000000000001`, issuer `https://auth.local.dev`,
audience `alur-chat`. Seed создаёт профили admin/alice/bob без перезаписи существующих;
роль определяется JWT, имя `admin` само по себе прав не даёт. Token живёт 15 минут.
Seed/dev-token отказывают при любом APP_ENV кроме development до доступа к БД/ключам.
Private key остаётся в `.local`, API получает только public key. Не печатать token в логи.

Реализовано: GET/PATCH `/api/v1/me`, GET `/api/v1/users`, GET `/api/v1/users/{user_id}`.
Первый валидный JWT атомарно создаёт пользователя с UUID-подобным display_name;
повтор/конкуренция сохраняют профиль. Внешняя идентичность уникальна внутри Project.
Все SQL запросы ограничены Project. Чужой UUID возвращает 404, public DTO скрывает
external_user_id и ban metadata. PATCH принимает только display_name/avatar_url;
avatar URL — HTTPS без userinfo, не скачивается сервером. Banned пользователь read-only.
Presence пока не работает: status=offline, last_seen_at=null до соответствующей фазы.
Пагинация UUID keyset: limit=1..100 (default 50), opaque next_cursor или null.

Контракт с DTO/ошибками: [OpenAPI](../api/openapi/openapi.json). Swagger UI — шаг 1.6.
Unit security matrix и real PostgreSQL tests входят в `scripts/test-infrastructure.ps1`:
конкурентный provisioning, межпроектная изоляция, actor injection, profile persistence,
ошибки JWT без создания строк и read-only ban. Production Auth/TLS интеграция отдельно.
