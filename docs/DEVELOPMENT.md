# Локальный development Compose

Compose предназначен только для разработки. Он не настраивает production TLS/HA и использует
legacy MinIO Community, риск поддержки которого описан в [INFRASTRUCTURE.md](INFRASTRUCTURE.md).
PostgreSQL, Redis, Kafka и MinIO не публикуют порты на хост; доступны API и worker probes.

## Первый запуск

Нужны Docker с Linux containers и Go toolchain из go.mod. В PowerShell из корня репозитория:

```powershell
$env:APP_ENV = 'development'
$env:CHAT_API_PORT = '18080' # Auth слушает 8080
$env:CHAT_WORKER_PORT = '18081'
go run ./cmd/dev-init
docker compose up -d --build --wait --wait-timeout 180
docker compose run --rm --build seed
curl.exe http://127.0.0.1:18080/health/ready
```

Linux/macOS: `APP_ENV=development go run ./cmd/dev-init`, затем та же команда Compose.
При конфликте портов задайте CHAT_API_PORT/CHAT_WORKER_PORT (по умолчанию 8080/8081).
Имя проекта при необходимости задаётся `docker compose -p <name>`; используйте его последовательно.

`dev-init` создаёт случайные пароли, RSA dev key pair и derived connection URLs в ignored `.local/`.
Он отказывается работать вне development, не ротирует существующие secrets и не трогает БД.
Каталог имеет mode 0700, файлы 0444 для non-root Docker secret mounts; в Windows доступ к каталогу
дополнительно ограничивается ACL ОС. Не размещайте секреты в общедоступном каталоге.
CHAT_SECRET_DIR позволяет использовать другой подготовленный каталог вместе с `dev-init --dir`.

Compose выполняет отдельные одноразовые jobs: migrate, minio-init, minio-account и kafka-init.
Kafka-init назначает корневой каталог нового volume uid 1000 без рекурсивного chown;
сам broker не запускается от root.
API/worker стартуют после них и после healthy dependencies. Runtime получает только свои secrets;
PostgreSQL DDL credentials доступны migrate/operator seed/user-role, MinIO root — только init jobs.
API/worker images — multi-stage Go build + distroless, uid/gid 65532, read-only root filesystem,
без capabilities, shell и package manager. Docker context исключает secrets, build и Agents.md.

## Остановка и обновление

```text
docker compose stop
docker compose up -d --build --wait --wait-timeout 180
```

Данные PostgreSQL, Redis AOF, Kafka и MinIO хранятся в volumes и сохраняются между перезапусками.
Миграции при повторном старте применяют только новые версии. Не удаляйте `.local/` при сохранённых
volumes: новые credentials не совпадут с инициализированными сервисами. Backup должен включать
секреты и данные; автоматического reset/удаления volumes нет. Команда `down` без `--volumes`
снимает контейнеры/сеть и сохраняет данные; `down --volumes` намеренно удаляет данные и не является
обычным способом обновления.

Ожидаемые завершённые jobs отображаются как Exited (0), API/worker — healthy.
SIGTERM инициирует HTTP drain и cleanup; Compose предоставляет 25 секунд до принудительной остановки.
Readiness пока подтверждает инфраструктуру; готовность полного чата зависит от следующих фаз.
По умолчанию JWT выдаёт внешний Auth на 8080, Chat использует AUTH_MODE=remote.
AUTH_BASE_URL из контейнера — http://host.docker.internal:8080 (Docker Desktop);
в других environments задайте доступный контейнеру доверенный URL явно.
AUTH_PROJECT_ID выбирает один заранее созданный Project на экземпляр API.
Роли Chat назначаются через user-role; login/refresh/logout остаются в Auth.
Offline dev-rsa включается явно; см. [IDENTITY.md](IDENTITY.md).
Swagger UI находится на API `/docs/api`, без Node/CDN dependency при запуске.
