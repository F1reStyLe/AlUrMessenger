# Attachments — реализованные шаги 6.1–6.2

`POST /api/v1/attachments` принимает `multipart/form-data` ровно с одной частью `file` и возвращает
201 Attachment со status=ready. Нужен валидный Actor human/bot; conversation membership до attach
не требуется. Проверяются Project active, ban, allow_images и для bot allow_bots.

Effective byte limit — минимум project `max_upload_size` (default 10 MiB) и
`GLOBAL_MAX_UPLOAD_SIZE` (default/max 50 MiB). Общий HTTP body limit default 64 MiB учитывает framing;
JSON routes независимо ограничены 1 MiB. Filename очищается до basename, control/invalid UTF-8 и
длина >255 bytes запрещены; имя не влияет на object key.

Разрешены JPEG `.jpg/.jpeg`, PNG `.png`, WebP `.webp`. Declared part MIME, extension, magic bytes
и decoder format должны совпасть. До полного decode проверяются maximum side 8192 и 16M pixels.
Полный decode выполняется с global concurrency limit. Точный JPEG EOI, PNG IEND или WebP RIFF size
должен совпасть с EOF: trailing script/executable/polyglot data запрещены.

После полной проверки БД получает storage_object и attachment в `uploading`, затем private MinIO
object записывается по случайному `project/{project_id}/attachments/{object_uuid}`. После ack обе
строки становятся `ready`. SHA-256/storage key наружу не выдаются, binary в PostgreSQL отсутствует.
При storage/DB failure explicit uploading state остаётся для worker reconciliation. В read-only container
staging использует `/tmp` tmpfs и удаляется после request.

`GET /api/v1/attachments/{id}` возвращает только safe metadata. Пока upload в `ready`, доступ имеет
только uploader и только до `expires_at`. После attach uploader-исключения больше нет: доступ
выводится из current membership живого message/conversation. Чужой Project, outsider,
deleted/expired message и terminal attachment скрываются одинаковым 404. Ban сохраняет read-only.

`POST /api/v1/attachments/{id}/download-url` повторяет эту проверку и выдаёт private S3 URL ровно
на минуту. `MINIO_PUBLIC_ENDPOINT` задаёт достижимый клиентом origin только для подписи; backend
пишет через внутренний `MINIO_ENDPOINT`. URL и storage key не сохраняются и не логируются.

IMAGE send принимает один собственный ready attachment и optional caption. Attachment transition,
message, sequence, blind index, dedup receipt и outbox фиксируются одной транзакцией. Exact retry
возвращает исходный SENT. IMAGE forward создаёт новый logical attachment и encrypted snapshot,
но разделяет immutable storage object. Удаление источника поэтому не ломает forward.

Worker переводит uploads старше часа и истёкшие unattached rows в durable `pending_delete`, удаляет
точный namespaced key идемпотентно и завершает `deleted`. Объект с attached reference не выбирается;
crash или MinIO failure оставляет `pending_delete` для повторного запуска.
