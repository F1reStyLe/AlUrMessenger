#!/bin/sh
# Fresh development database only; passwords are supplied as separate secret files.
set -eu
runtime_password=$(cat /run/secrets/pg_runtime_password)
migration_password=$(cat /run/secrets/pg_migration_password)
psql -v ON_ERROR_STOP=1 --username postgres --dbname alur \
  --set=runtime_password="$runtime_password" --set=migration_password="$migration_password" <<'SQL'
CREATE ROLE alur_runtime LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD :'runtime_password';
CREATE ROLE alur_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD :'migration_password';
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE CREATE ON DATABASE alur FROM PUBLIC;
GRANT CREATE, CONNECT ON DATABASE alur TO alur_migrator;
GRANT USAGE, CREATE ON SCHEMA public TO alur_migrator;
SQL
