#!/usr/bin/env bash
# Restore drill for backup.sh (P1-003): the newest backup in R2 is decrypted
# and restored into a throwaway PostgreSQL on this box, and its row counts are
# compared with the live database. Nothing live is written.
#   restore-drill.sh
#
# The checklist asks for the drill on a clean machine; this proves the backup,
# the passphrase and the restore path, not the box. A real restore goes into a
# new, empty provider database as its admin role: README.md, "Restoring into a
# limited-mode database" (in the default mode, first app_user with
# db-preflight.sh --apply, and CREATE ROLE auth_lookup NOLOGIN BYPASSRLS).
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck source=deploy/backup-env.sh
source ./backup-env.sh

major=$(server_major)
latest=$(rclone lsf --files-only "r2:$BACKUP_S3_BUCKET/pg/" | sort | tail -n 1)
[[ -n $latest ]] || { echo "no backups in r2:$BACKUP_S3_BUCKET/pg/" >&2; exit 1; }
echo "drilling $latest against postgres $major"

work=$(mktemp -d)
drill=restore-drill-$$
cleanup() {
	docker rm -f "$drill" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT

rclone cat "r2:$BACKUP_S3_BUCKET/pg/$latest" |
	openssl enc -d -aes-256-cbc -pbkdf2 -iter 200000 -pass env:BACKUP_PASSPHRASE >"$work/db.dump"

docker run -d --name "$drill" -e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_DB=drill \
	-v "$work:/backup:ro" "postgres:$major" >/dev/null
for _ in $(seq 60); do docker exec "$drill" pg_isready -q -U postgres -d drill && break; sleep 1; done
sleep 2

# The drill counts rows, so it restores without owners or grants: those name
# the provider's admin role, which does not exist here. The two roles the
# schema itself creates are made first so its role-checking DO blocks pass.
docker exec "$drill" psql -U postgres -d drill -Xq -v ON_ERROR_STOP=1 \
	-c "CREATE ROLE app_user LOGIN" -c "CREATE ROLE auth_lookup NOLOGIN BYPASSRLS"
docker exec "$drill" pg_restore -U postgres -d drill --no-owner --no-acl --exit-on-error /backup/db.dump

tables=(tenants users products variants orders customers audit_log)
count_sql() {
	local t sep="" sql="SELECT "
	for t in "${tables[@]}"; do sql+="$sep(SELECT count(*) FROM $t)"; sep=" || ' ' || "; done
	echo "$sql"
}
# The live counts are read as the admin role. If it is not a superuser, FORCE
# ROW LEVEL SECURITY hides every tenant's rows from it; the drill's superuser
# sees them all. So only tenants (no RLS) is compared exactly.
read -r -a restored < <(docker exec "$drill" psql -U postgres -d drill -XAtqc "$(count_sql)")
read -r -a live < <(docker run --rm --network host -e DBURL "postgres:$major" sh -c "exec psql \"\$DBURL\" -XAtqc \"$(count_sql)\"")

printf '%-12s %10s %10s\n' table restored live
for i in "${!tables[@]}"; do
	shown=${live[$i]}
	# Limited mode: the owner is bound by FORCE ROW LEVEL SECURITY, so only
	# tenants (no RLS) can be counted live.
	[[ $DB_ROLE_MODE == limited && $i -gt 0 ]] && shown="-"
	printf '%-12s %10s %10s\n' "${tables[$i]}" "${restored[$i]}" "$shown"
done
[[ $DB_ROLE_MODE == limited ]] && echo "(limited-roles mode: live counts other than tenants are hidden by RLS)"
if [[ ${restored[0]} != "${live[0]}" ]]; then
	echo "FAIL: tenants differ" >&2
	exit 1
fi
echo "PASS: $latest restores; rows written since it was taken account for any gap"
