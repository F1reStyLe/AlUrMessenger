-- +goose Up
-- Схема принадлежит migration account. Runtime role создаётся оператором до миграций.
-- Domain tables появятся с Project/User в 1.4, фиктивные бизнес-таблицы не создаём.
CREATE SCHEMA chat;
REVOKE ALL ON SCHEMA chat FROM PUBLIC;
GRANT USAGE ON SCHEMA chat TO alur_runtime;
GRANT SELECT ON public.goose_db_version TO alur_runtime;
-- Будущие таблицы того же migration owner доступны runtime для DML, но не DDL.
ALTER DEFAULT PRIVILEGES IN SCHEMA chat GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO alur_runtime;
ALTER DEFAULT PRIVILEGES IN SCHEMA chat GRANT USAGE, SELECT ON SEQUENCES TO alur_runtime;

-- +goose Down
-- Без CASCADE: не удаляем неизвестные таблицы/данные при несовместимом откате.
ALTER DEFAULT PRIVILEGES IN SCHEMA chat REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM alur_runtime;
ALTER DEFAULT PRIVILEGES IN SCHEMA chat REVOKE USAGE, SELECT ON SEQUENCES FROM alur_runtime;
DROP SCHEMA chat;
