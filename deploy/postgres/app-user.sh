#!/bin/sh
# Runs once, on an empty data directory, before the first migration: the role
# the API and the worker connect as, with its password. Migration 1 finds it
# and skips its own CREATE ROLE, so no password is ever set by hand.
set -eu
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
	-v pw="$APP_DB_PASSWORD" <<'SQL'
CREATE ROLE app_user LOGIN PASSWORD :'pw';
SQL
