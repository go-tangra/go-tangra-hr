#!/bin/sh
# First start of the bundle's database: the extensions hr's migrations need
# and the application role (no BYPASSRLS: row-level security backs every
# tenant predicate; migrations only grant to it). Its password is generated
# by the gateway (hex, so it needs no quoting).
set -eu
psql -v ON_ERROR_STOP=1 --username postgres --dbname hr <<SQL
CREATE EXTENSION IF NOT EXISTS timescaledb;
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE ROLE hr_app LOGIN PASSWORD '${HR_APP_PASSWORD}' NOBYPASSRLS;
GRANT CONNECT ON DATABASE hr TO hr_app;
SQL
