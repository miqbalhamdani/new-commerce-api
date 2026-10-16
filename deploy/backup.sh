#!/usr/bin/env bash
# Nightly logical backup of a provider-managed PostgreSQL (compose.managed-db.yaml),
# kept off the provider: pg_dump -> AES-256 -> the R2 backups bucket.
#   backup.sh                      (systemd/new-commerce-backup.timer runs it nightly)
#
# This is P1-003 for a managed database, where pgBackRest cannot run: the
# provider has the WAL and the data directory. The provider's own backups are
# the first line; this copy is the one that survives losing the provider
# account. RPO is a day unless the provider's point-in-time restore covers it.
#
# Reads from .env: MIGRATE_DATABASE_URL, BACKUP_PASSPHRASE, BACKUP_S3_ENDPOINT
# (https://<account>.r2.cloudflarestorage.com), BACKUP_S3_ACCESS_KEY,
# BACKUP_S3_SECRET_KEY, BACKUP_S3_BUCKET. Use a token scoped to the backups
# bucket alone, so the app's own key cannot delete a backup. Expiry is an R2
# lifecycle rule on the bucket, not this script.
#
# Without BACKUP_PASSPHRASE every backup is unreadable: keep it in the vault.
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck source=deploy/backup-env.sh
source ./backup-env.sh

major=$(server_major)
name="pg/new_commerce-$(date -u +%Y%m%dT%H%M%SZ).dump.enc"

# Custom format (pg_restore can pick tables back out of it), compressed by
# pg_dump; openssl only encrypts.
docker run --rm --network host -e DBURL "postgres:$major" \
	sh -c 'exec pg_dump --format=custom --compress=6 "$DBURL"' |
	openssl enc -aes-256-cbc -pbkdf2 -iter 200000 -salt -pass env:BACKUP_PASSPHRASE |
	rclone rcat "r2:$BACKUP_S3_BUCKET/$name"

size=$(rclone size --json "r2:$BACKUP_S3_BUCKET/$name" | sed -E 's/.*"bytes":([0-9]+).*/\1/')
if ((size < 1024)); then
	echo "backup $name is only $size bytes" >&2
	exit 1
fi
echo "backed up $name ($size bytes, postgres $major)"
