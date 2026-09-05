# Attachments — реализованный шаг 6.1

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
При storage/DB failure explicit uploading state остаётся для sweeper 6.3. В read-only container
staging использует `/tmp` tmpfs и удаляется после request.

GET metadata, short authorized download URL, message association и IMAGE send — шаг 6.2.
