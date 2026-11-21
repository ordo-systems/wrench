#!/bin/sh
# Runs once when the data volume is created: a login role that can only read, used by the backup container.
set -eu
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" -v pw="$WRENCH_BACKUP_DB_PASSWORD" <<'SQL'
CREATE ROLE wrench_backup LOGIN PASSWORD :'pw';
GRANT pg_read_all_data TO wrench_backup;
SQL
