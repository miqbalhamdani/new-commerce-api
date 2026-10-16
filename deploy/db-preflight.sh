#!/usr/bin/env bash
# Checks a provider-managed PostgreSQL (compose.managed-db.yaml) can take this
# schema, before the first migrate runs against it:
#   db-preflight.sh           report only; changes nothing
#   db-preflight.sh --apply   default mode: also create app_user (or reset its
#                             password). Limited mode has no app_user.
#
# Reads MIGRATE_DATABASE_URL (the provider's admin role, which will own the
# schema), DB_ROLE_MODE and APP_DB_PASSWORD from .env. Every probe that needs
# a write runs in a transaction that is rolled back.
#
# What the migrations need from the role that runs them, in the default mode:
#   - the extensions ltree, unaccent, pg_trgm, citext (000001)
#   - CREATE ROLE app_user LOGIN (000001) -- CREATEROLE
#   - CREATE ROLE auth_lookup NOLOGIN BYPASSRLS (000003, 000006, 000022) --
#     superuser, or BYPASSRLS on the creator itself. The four SECURITY DEFINER
#     lookups (CLAUDE.md, "Tenancy") belong to that role; without it, sign-in
#     cannot work under FORCE ROW LEVEL SECURITY.
# DB_ROLE_MODE=limited (BR-001) needs neither role, and is only for a database
# that cannot provide them: the app then runs as the schema owner.
set -euo pipefail
cd "$(dirname "$0")"

apply=0
[[ ${1:-} == --apply ]] && apply=1

# Not `source .env`: Compose's format allows unquoted spaces (TLS=...).
env_get() { { grep -E "^$1=" .env || true; } | tail -n 1 | cut -d= -f2-; }
MIGRATE_DATABASE_URL=${MIGRATE_DATABASE_URL:-$(env_get MIGRATE_DATABASE_URL)}
DB_ROLE_MODE=${DB_ROLE_MODE:-$(env_get DB_ROLE_MODE)}
DB_ROLE_MODE=${DB_ROLE_MODE:-strong}
APP_DB_PASSWORD=${APP_DB_PASSWORD:-$(env_get APP_DB_PASSWORD)}
: "${MIGRATE_DATABASE_URL:?set MIGRATE_DATABASE_URL in .env}"
case $DB_ROLE_MODE in
strong) : "${APP_DB_PASSWORD:?set APP_DB_PASSWORD in .env}" ;;
limited) ;;
*) echo "DB_ROLE_MODE is $DB_ROLE_MODE; want strong or limited" >&2; exit 1 ;;
esac
# Secrets reach containers as `-e NAME`, never `-e NAME=value` (visible in ps).
export DBURL=$MIGRATE_DATABASE_URL PW=${APP_DB_PASSWORD:-}

# psql from postgres:18, so the client is never older than the server.
psql() {
	docker run --rm -i --network host -e DBURL -e PGCONNECT_TIMEOUT=10 postgres:18 \
		sh -c 'exec psql "$DBURL" -X -v ON_ERROR_STOP=1 -qAt "$@"' psql "$@"
}

fail=0
pass() { printf 'PASS  %s\n' "$1"; }
bad() { printf 'FAIL  %s\n' "$1"; fail=1; }
warn() { printf 'WARN  %s\n' "$1"; }
info() { printf 'INFO  %s\n' "$1"; }

echo "      DB_ROLE_MODE=$DB_ROLE_MODE"
version=$(psql -c 'SHOW server_version_num') || { echo "cannot connect with MIGRATE_DATABASE_URL" >&2; exit 1; }
if ((version >= 160000)); then pass "server version $version (CI runs 16 and 18)"; else bad "server version $version: need 16 or later"; fi

read -r super createrole bypass < <(psql -F ' ' -c \
	"SELECT rolsuper, rolcreaterole, rolbypassrls FROM pg_roles WHERE rolname = current_user")
echo "      role: superuser=$super createrole=$createrole bypassrls=$bypass"

if psql -c "BEGIN; CREATE EXTENSION IF NOT EXISTS ltree; CREATE EXTENSION IF NOT EXISTS unaccent;
            CREATE EXTENSION IF NOT EXISTS pg_trgm; CREATE EXTENSION IF NOT EXISTS citext; ROLLBACK" >/dev/null 2>&1; then
	pass "extensions ltree, unaccent, pg_trgm, citext can be created"
else
	bad "cannot create the extensions ltree, unaccent, pg_trgm, citext"
fi

can_login=0 can_bypass=0
psql -c "BEGIN; CREATE ROLE preflight_login LOGIN; ROLLBACK" >/dev/null 2>&1 && can_login=1
psql -c "BEGIN; CREATE ROLE preflight_bypass NOLOGIN BYPASSRLS; ROLLBACK" >/dev/null 2>&1 && can_bypass=1
if [[ $DB_ROLE_MODE == strong ]]; then
	if ((can_login)); then pass "can create a LOGIN role (app_user)"; else bad "cannot create roles: the migrate role needs CREATEROLE (or DB_ROLE_MODE=limited, BR-001)"; fi
	if ((can_bypass)); then pass "can create a BYPASSRLS role (auth_lookup)"; else bad "cannot create a BYPASSRLS role (auth_lookup): ask the provider for BYPASSRLS on this role (or DB_ROLE_MODE=limited, BR-001)"; fi
elif ((can_login && can_bypass)); then
	bad "this role can create app_user and auth_lookup: use the default mode, not DB_ROLE_MODE=limited"
else
	info "limited-roles mode: no app_user or auth_lookup; the app connects as this role (BR-001)"
	if [[ $super == t || $bypass == t ]]; then
		bad "this role is a superuser or has BYPASSRLS: FORCE ROW LEVEL SECURITY would not bind the app"
	else
		pass "this role is bound by FORCE ROW LEVEL SECURITY"
	fi
fi

if [[ $(psql -c "SELECT has_schema_privilege('public', 'CREATE')") == t ]]; then
	pass "can create tables in schema public"
else
	bad "no CREATE on schema public"
fi

# A schema already here was frozen into one mode by its first migration.
existing=$(psql -c "SELECT CASE WHEN to_regprocedure('db_role_mode()') IS NULL THEN '' ELSE db_role_mode() END")
if [[ -z $existing ]]; then
	info "no schema yet: the first migrate creates it in $DB_ROLE_MODE mode"
elif [[ $existing == "$DB_ROLE_MODE" ]]; then
	pass "existing schema is in $existing mode"
else
	bad "existing schema is in $existing mode but DB_ROLE_MODE is $DB_ROLE_MODE"
fi

# A transaction-mode pooler gives each transaction whichever server connection
# is free; five autocommit statements on one client connection then report
# more than one backend.
pids=$(psql -c 'SELECT pg_backend_pid()' -c 'SELECT pg_backend_pid()' -c 'SELECT pg_backend_pid()' \
	-c 'SELECT pg_backend_pid()' -c 'SELECT pg_backend_pid()' | sort -u | wc -l)
if ((pids == 1)); then pass "one backend per connection (no transaction pooler)"; else bad "statements landed on $pids backends: this is a transaction-mode pooler, use the direct port"; fi

ms=$(psql -c '\timing on' -c 'SELECT 1' -c 'SELECT 1' -c 'SELECT 1' | awk '/^Time:/ { s += $2; n++ } END { if (n) printf "%.1f", s / n }')
echo "      round trip: ${ms:-?} ms per query"

maxconn=$(psql -c 'SHOW max_connections')
if ((maxconn >= 40)); then pass "max_connections $maxconn"; else warn "max_connections $maxconn: two API pools and the worker want a few dozen"; fi

if [[ $(psql -c 'SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()') == t ]]; then
	pass "this connection is encrypted"
else
	warn "this connection is NOT encrypted: every query and row crosses the network in clear text"
fi

if ((fail)); then
	echo "preflight failed: fix the above before running migrate" >&2
	exit 1
fi

if [[ $DB_ROLE_MODE == limited ]]; then
	echo "preflight passed; nothing to --apply in limited mode (no app_user)"
elif ((apply)); then
	# The managed stand-in for postgres/app-user.sh: migration 1 finds the role
	# and skips its own CREATE ROLE.
	docker run --rm -i --network host -e DBURL -e PW postgres:18 \
		sh -c 'psql "$DBURL" -X -v ON_ERROR_STOP=1 -q -v pw="$PW"' <<'SQL'
SELECT format(CASE WHEN EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_user')
                   THEN 'ALTER ROLE app_user LOGIN PASSWORD %L'
                   ELSE 'CREATE ROLE app_user LOGIN PASSWORD %L' END, :'pw') \gexec
SQL
	echo "app_user ready"
else
	echo "preflight passed; run again with --apply to create app_user"
fi
