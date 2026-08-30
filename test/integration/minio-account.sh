#!/bin/sh
# Credentials не выводятся; IAM policy scoped к одному bucket и не даёт SetBucketPolicy.
set -eu
admin_password=$(cat /run/test-secrets/minio-admin)
runtime_password=$(cat /run/test-secrets/minio-runtime)
attempt=0
until mc --config-dir /tmp/mc alias set local http://minio:9000 test-root "$admin_password" >/dev/null 2>&1; do
  attempt=$((attempt+1))
  [ "$attempt" -lt 90 ] || exit 1
  sleep 1
done
mc --config-dir /tmp/mc admin user add local test-runtime "$runtime_password" >/dev/null
mc --config-dir /tmp/mc admin policy create local alur-runtime /init/policy.json >/dev/null
mc --config-dir /tmp/mc admin policy attach local alur-runtime --user test-runtime >/dev/null
