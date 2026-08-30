#!/bin/sh
# Only dev provisioning has root credentials. Runtime IAM never receives them.
set -eu
admin_password=$(cat /run/secrets/minio_admin_password)
runtime_password=$(cat /run/secrets/minio_runtime_password)
attempt=0
until mc --config-dir /tmp/mc alias set local http://minio:9000 alur-admin "$admin_password" >/dev/null 2>&1; do
  attempt=$((attempt+1))
  [ "$attempt" -lt 60 ] || exit 1
  sleep 1
done
mc --config-dir /tmp/mc admin user add local alur-runtime "$runtime_password" >/dev/null
mc --config-dir /tmp/mc admin policy create local alur-runtime /init/policy.json >/dev/null
mc --config-dir /tmp/mc admin policy attach local alur-runtime --user alur-runtime >/dev/null
