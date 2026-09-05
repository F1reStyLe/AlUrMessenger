# Модель безопасности MVP

Статус: обязательные требования к новой реализации, Phase 0. Документ не является заявлением
о проведённом security review готового приложения. Контракты: [API](API_DESIGN.md),
[WS](WEBSOCKET_PROTOCOL.md), [events](EVENTS.md), [schema](DB_SCHEMA.md).
Реализованный Foundation subset: [JWT/identity](docs/IDENTITY.md),
[Project permissions, audit, CORS и Redis quotas](docs/POLICIES.md).
Реализованы также AES-GCM/AAD и blind search без plaintext-копии ([Messages](docs/MESSAGES.md)),
current-membership replay, отзыв WS-сессий через Auth и ephemeral privacy ([Realtime](docs/REALTIME.md)).
Шаг 5.3 проверяет source/target independently и не раскрывает source conversation в forward DTO.
Это не внешний security audit и не production TLS/HA deployment.

## Trust boundaries

Клиент, JWT до verification, HTTP headers, WebSocket payload, metadata, filenames и webhook URL
недоверенные. Внешний Auth доверен только как настроенный источник идентичностей. Сетевое
расположение gRPC/Kafka внутри Docker network само по себе не является аутентификацией.

Пользователь одного Project не должен читать/изменять ресурс другого, даже зная UUID.
Tenant scope обязателен в Actor, repository SQL, FK, cache keys, events и object keys.
Raw project_id/user_id от клиента не подменяют проверенного actor. Доступ к source и target
reply/forward/attachment проверяется независимо; cross-project forward запрещён.

## Authentication и секреты

- Remote JWT проверяется Auth через GET users/me на каждом запросе: signature/expiry/session/account.
  Нет positive cache, локального signature-only fallback и передачи signing secret в Chat.
  HTTP timeout 2s, bounded bodies/headers, redirects/proxies отключены, HTTPS в production.
  Auth 401/403 даёт Chat 401, недоступность/неожиданный ответ — 503, без допуска пользователя.
- AUTH_BASE_URL/AUTH_PROJECT_ID задаёт оператор; один Auth и Project на текущий API deployment.
  Произвольные JWT jku/x5u/kid не выбирают endpoint. Не перепривязывать существующий Project
  к независимому Auth: совпавшие user ID могут дать доступ к чужому профилю.
- Offline dev-rsa только development/test: RS256, trusted issuer/audience/Project, exp/sub,
  token_use=access, nbf/iat при наличии, 30s skew. Он не обеспечивает отзыв внешней сессии.
- `external_user_id` — строковый ID из подтверждённого ответа Auth (sub в offline fixture).
  user/admin хранится в Chat users.role; roles Auth/JWT игнорируются. Runtime DB не может
  изменять роль, operator CLI работает только с существующим scoped профилем. Auto-provision конкурентобезопасен
  и не принимает admin/moderator role из profile payload. Display name генерируется случайно.
- API keys — random 256 bits, hash-only SHA-256 storage, prefix для поиска и безопасной идентификации.
  Scopes, project, expiry/revocation проверяются на каждой операции. Secret показывается один раз.
- Internal gRPC — mTLS, known service identity, Project allowlist и capabilities. Bot credential
  привязан к bot user, не позволяет присвоить себе другого sender. SYSTEM требует system:write.
- Master encryption/search keys, private JWT dev key, MinIO/DB credentials и webhook secret
  не коммитятся. Поддержать environment/secret files, permissions на files, отсутствие secret в argv/logs.
- Приложение не хранит пароли пользователей, не выпускает production JWT и не принимает refresh tokens.

## Матрица полномочий

| Действие | Member | Moderator | Project admin | Bot/service |
| --- | --- | --- | --- | --- |
| Читать свой DIRECT/GROUP/CHANNEL | Active membership | То же | Обычный API — то же; admin API — scoped и audited | Bot membership или отдельная service read capability |
| Создать DIRECT/GROUP/CHANNEL | Human JWT, не banned | То же | То же | Не общая возможность service key |
| Писать в DIRECT/GROUP | Active membership | Да | Только как участник либо SYSTEM через privileged path | Bot membership + allowlist + allow_bots |
| Писать в CHANNEL | Нет | Active moderator | Member/moderator либо privileged SYSTEM | Bot moderator + allowlist |
| Edit собственного сообщения | Автор, flag | Только своё | Не переписывает чужой content | Bot только своё, SYSTEM запрещён |
| Delete собственного сообщения | Автор, flag | То же | То же | Bot только своё |
| Добавить/снять свою реакцию | Active membership, включая CHANNEL reader; flag | То же | Только собственная reaction | Membership + allow_bots |
| Закрепить/открепить сообщение | Нет | Только GROUP/CHANNEL, allow_pin | Только как moderator | Bot moderator + allow_bots |
| Удалить нарушение | Нет | Свой GROUP/CHANNEL, reason + audit | В Project через admin API | Только явно privileged moderation service |
| Назначить moderator/управлять участниками | Нет | Свой GROUP/CHANNEL, last-moderator guard | Scoped admin capability | Нет по обычному bot key |
| Ban/unban Project user, blacklist, reports review | Нет | Не global ban | Да, audit | Только admin capability |
| API keys/webhooks/settings/audit | Нет | Нет | Да; audit read-only | Выделенные scopes либо admin, без самоэскалации |

Global ban запрещает domain writes, включая profile, creation, messages, reactions, reports,
checkpoints и typing. Group/channel membership ban ограничивает writes только этого conversation.
Leave/kick отзывает чтение, включая выдачу новых download URLs; не удаляет историю остальных.
Blacklist 7.1 проверяется внутри общей message transaction после Project/actor/access locks и до
sequence/ciphertext/index/outbox mutation. NFC + Unicode case folding и whole letter/digit tokens
исключают case bypass; substring сознательно не блокируется. 422 и audit не содержат matched word
или message content. Human, bot, internal SYSTEM, IMAGE caption, edit и forward используют один gate.

Запрет записи не мешает heartbeat, reconnect и ранее разрешённому чтению. Смена роли/moderation
синхронизируется через общий transaction lock protocol, а не только через кеш.

## Content encryption и поиск

Message payload: AES-256-GCM, случайный уникальный nonce на encryption, 256-bit key из secret store.
AAD включает purpose, project_id, conversation_id, message_id, payload_version, key_version.
Расшифровка чужого ciphertext в другом resource context должна завершаться ошибкой authentication tag.
EncryptionProvider абстрагирует storage; ошибки ключа/формата → fail closed, без plaintext fallback.
Webhook secrets и report descriptions используют отдельный purpose/AAD; encrypted envelope
хранит nonce/key_version/ciphertext, но никогда сам ключ. Изображения защищаются private MinIO
и encryption at rest object-store/disk; production storage encryption настраивается отдельно.

Message headers (sender, conversation, timestamps, type, sizes) остаются видимыми БД: это не E2EE.
Сервер и operator с доступом к ключам могут читать content для moderation/search. Backup с ciphertext
и backup ключей хранятся раздельно; восстановление и потеря/rotation ключей документируются.

Search index — HMAC-SHA256 от normalized full token, отдельный search master key, derive через
HKDF-SHA256 с project_id/purpose/version. Разные Projects не получают одинаковых токенов для одного
слова. PostgreSQL GIN ищет AND tokens, затем service авторизует/расшифровывает DTO.

**Компромисс:** blind index раскрывает равенство и частотность слов внутри Project, число токенов,
связь сообщений с одинаковыми словами. При утечке search key возможна dictionary атака.
Это не семантически защищённый searchable encryption. Plaintext FTS/search representation отсутствует.
MVP не поддерживает substrings/fuzzy/stemming; такой API не обещается. При edit индекс меняется
в той же transaction, при delete удаляется, expired messages исключаются до физической очистки.

## Transport, input и resource limits

- Внешние production endpoints HTTPS/WSS; internal gRPC mTLS. Development HTTP только на loopback
  и явно разрешённой изолированной Docker network. Не выставлять dev Compose в Интернет.
- CORS/origins — exact allowlist. Credentialed wildcard запрещён. WS Origin проверяется отдельно.
  Browser JWT хранить в памяти Demo, не в URL/localStorage. Refresh делает внешний Auth.
- HTTP body/frame/count/string bounds, timeouts и cancellation. SQL только parameterized.
  Unknown properties/invalid JSON/null вместо command отклоняются до application use case.
- Redis rate limits per IP, authenticated user и API key; при trusted proxy IP принимать только
  из настроенного proxy range. Нельзя доверять произвольному X-Forwarded-For.
- При Redis outage write paths fail closed или используют заранее проверенный строгий local fallback;
  MVP выбирает fail closed. Историю можно читать при локальном bounded read limit, auth и рабочей БД.
- Каждый socket имеет bounded queue, срок auth, token expiry, read/write deadlines и контролируемый
  shutdown. Превышение limits не приводит к неограниченному росту goroutines/memory.
- API возвращает text как data, не HTML. Frontend использует escaping, не v-html для message content;
  небезопасные URL schemes запрещены. Avatar URL сервер не скачивает произвольно.

## Files и MinIO

Allowlist JPEG/PNG/WebP, actual magic bytes + согласованный extension/MIME, валидное decode header/
image. Не доверять клиентскому Content-Type. Ограничить bytes (default 10 MiB), dimensions и pixels
(начально максимум 8192 по стороне и 16 megapixels), concurrency декодирования и timeout.
SVG/HTML/executable/polyglot/повреждённые файлы отклоняются. При нормализации изображения удалять
необязательные metadata/EXIF; не предоставлять непроверенный upload как ready attachment.

Реализация 6.1 принимает ровно один multipart file, сначала применяет effective Project/global
byte limit, затем сверяет extension, declared MIME, magic и точный конец контейнера. DecodeConfig
ограничивает 8192/16M до полного decode; decode concurrency bounded. Staging идёт в mode 0600 tmpfs,
после запроса файл удаляется. JSON сохраняет отдельный 1 MiB limit. SVG/GIF и trailers запрещены.

Private bucket, случайные namespaced keys; original_name не влияет на object path. Attach разрешён
только uploader/разрешённому use case в том же Project. Новый attachment не может ссылаться на
pending-delete object. Signed URL выдаётся только после повторной authorization, TTL ≤ 60 секунд,
Content-Disposition безопасен, URL не пишется в log/referrer. Уже выданная ссылка действует до TTL:
если нужен мгновенный отзыв, будущая версия использует backend-proxied download.

Реализация 6.2 после attach полностью заменяет uploader access проверкой current membership живого
message. Подпись использует отдельный `MINIO_PUBLIC_ENDPOINT` (HTTPS обязателен production), тогда
как запись идёт через private internal endpoint. Cleanup получает exact Project-scoped key из БД,
не перечисляет bucket и не выбирает object с attached reference.

Retention/cleanup не удаляет object, пока существует живая message reference (включая forward).
Worker повторяет delete по durable job; crash между DB/MinIO операциями не даёт доступа к чужому файлу.

## Webhook SSRF и подпись

HTTPS по умолчанию, разрешённый port 443, без URL userinfo. При регистрации и каждой попытке
разрешать DNS, запрещать loopback/private/link-local/multicast/metadata destinations и IPv4-mapped
IPv6 обходы. HTTP client соединяется с проверенным IP, сохраняя исходный hostname для TLS/SNI;
не выполнять второе неконтролируемое DNS разрешение. Redirects отключены, response bytes/time bounded.

Для local test endpoint допускается отдельный явно включённый allow-private switch **только**
при APP_ENV=development; production validation запрещает его. Proxy settings не должны обходить
проверку destination. HMAC подписывает timestamp + raw_body; получатель проверяет timestamp,
constant-time signature и event_id. Retries не гарантируют порядок или exactly-once внешнего side effect.

## Audit и эксплуатация

Audit содержит actor/project/action/resource/time/request_id и whitelisted metadata. Изменение
пользовательской роли, ban, blacklist, API keys, webhook config, settings и moderation атомарно
записываются вместе с доменным изменением. Чувствительное admin read тоже журналируется.
Secrets, content, signed URLs и произвольный request body в audit не допускаются.

slog correlation: request_id, event_id, connection_id. Не логировать authorization headers,
auth frames, DSN с password или ответ webhook. Sanitize ошибки adapters перед логированием/выдачей.
API account не имеет DDL прав; migration account отдельный. Kafka/Redis/MinIO доступны только
необходимым компонентам, production authentication/ACL включены.

fsync/synchronous_commit PostgreSQL не отключать ради скорости. Долговечность accepted message
требует надёжных storage/backup/replication; Compose на одном диске не обещает нулевой RPO при потере
этого диска. Health/readiness не заменяют restore drill и fault tests.

## Development и обязательные проверки

Dev token/seed/key initialization проверяют `APP_ENV=development` внутри команды, не только в UI.
В production отсутствуют dev endpoints, insecure TLS switches и default shared secrets.
Compose development генерирует уникальные локальные secrets в игнорируемом secret storage;
production получает готовые secrets от оператора и отказывается стартовать без них.

Негативные tests по фазам: Project A→B, impersonation user_id, неверные iss/aud/exp/alg/kid,
refresh JWT как access, race ban/send, membership leave/delivery, disabled flags через REST/WS/gRPC/bot,
cross-project reply/forward/files, image bombs/invalid files, SSRF DNS rebinding, revoked API key,
duplicate events/outbox crash, encryption AAD tampering и sensitive log redaction.
Hardening завершает security review, но эти проверки не откладываются целиком до Phase 10.
