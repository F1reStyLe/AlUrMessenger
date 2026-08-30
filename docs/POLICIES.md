# Project policies — Foundation 1.5

Только пользователь с ролью admin в БД Chat может GET/PATCH `/admin/v1/project`,
GET/PATCH `/admin/v1/feature-flags` и GET `/admin/v1/audit-logs` своего Project.
Role user получает 403; moderator не является глобальной ролью. Banned admin может
читать, но не писать. Operator пока управляет ban через БД; публичный ban API относится
к последующей фазе moderation. Клиент не задаёт Project/actor в payload.
JWT доказывает идентичность через Auth, но не назначает права. Роль читается при каждом
запросе и повторно проверяется под lock при записи. Операторское назначение/понижение
через user-role описано в [Identity](IDENTITY.md); перевыпуск JWT не нужен.

Оба GET возвращают единый Settings DTO. PATCH project принимает частичные flags,
max_upload_size (1..52428800 bytes), message_retention_days (1..3650); PATCH feature-flags
принимает только flags. Обязателен expected_version как десятичная строка. Неверные поля,
null flags, пустое изменение или выход за bounds дают 400; старая версия — 409.
Пример: `{"expected_version":"1","flags":{"allow_images":false}}`.
После успеха settings_version увеличивается. Конкурирующие обновления сериализуются
Project row lock; только одно изменение с данной версией проходит.

Defaults: allow_images/reactions/edit/delete/forward/reply/pin, typing_enabled,
presence_enabled/read_receipts=true; allow_bots/allow_webhooks=false. Dev seed включает
последние два только до первого admin edit. Upload=10 MiB, retention=365 дней.
Эти настройки не реализуют отправку сообщений/файлов сами по себе: будущие use cases
должны вызывать RequireFeature на snapshot под shared Project lock своей транзакции.
Изменение retention не запускает удаление и не меняет существующие сроки хранения.

Settings mutation и audit insert фиксируются одной transaction: ошибка audit откатывает
settings. Metadata содержит только имена изменённых полей, не значения/секреты/контент.
Audit runtime role имеет INSERT/SELECT без UPDATE/DELETE; database operator остаётся
доверенным и технически может изменять БД — immutable compliance storage не обещается.
Audit API поддерживает cursor/limit (1..100), forward UUID v7 ID order, не порядок commit.
Расширенные actor/action/time filters добавятся с admin-функциональностью; сейчас их
передача отвергается как неизвестные параметры. Чужие audit entries не выдаются.

HTTP admission: per-IP Redis limit → CORS → JWT/provisioning → Project-user Redis limit
→ authorization/use case. Fixed window 60 секунд с первого запроса; Redis Lua атомарно
увеличивает счётчик и задаёт TTL. Defaults: 120/IP, 60/Project-user в минуту, настройки
RATE_IP_PER_MINUTE/RATE_USER_PER_MINUTE = 1..100000. Все API instances используют Redis.
429 содержит Retry-After в секундах; Redis failure — 503, без local fallback.
Probes/docs не расходуют quotas. IP берётся из RemoteAddr; X-Forwarded-For/Forwarded
игнорируются. За reverse proxy все пользователи делят его IP quota до отдельной настройки
доверенных proxies; это ограничение нужно учесть при production deployment.

CORS_ALLOWED_ORIGINS — exact comma-separated origins без path, wildcard, userinfo;
production допускает только HTTPS. Пустой список запрещает cross-origin requests.
Requests без Origin разрешены (CORS не заменяет JWT). Unknown Origin → 403, без ACAO.
OPTIONS preflight не требует JWT, но учитывается в IP quota; allowed headers:
Authorization, Content-Type, X-Request-ID. Cookie credentials не включаются.
Compose задаёт dev UI origins и свой API origin; остальные deployments задают их явно.

Проверки: unit validation, реальные PostgreSQL/Redis tests, permissions/tenant isolation,
version conflict, audit rollback/append-only privileges, CORS, 429/Retry-After и fail-closed.
Контракт операций/DTO: [OpenAPI](../api/openapi/openapi.json).
