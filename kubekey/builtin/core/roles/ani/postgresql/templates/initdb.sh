#!/bin/sh
# ANI PostgreSQL first-init script (mounted into /docker-entrypoint-initdb.d).
# Runs once, only when PGDATA is empty. Creates the non-superuser application
# account and its database. Real authentication is proven later by the
# component verify job over the Service DNS, not by this trust-local init.
set -e

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres \
  -v ani_app_pw="$ANI_APP_PASSWORD" <<-EOSQL
CREATE USER ani_app WITH PASSWORD :'ani_app_pw' NOSUPERUSER NOCREATEDB NOCREATEROLE;
CREATE DATABASE ani OWNER ani_app;
GRANT ALL PRIVILEGES ON DATABASE ani TO ani_app;
EOSQL
