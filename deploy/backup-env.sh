# shellcheck shell=bash
# Sourced by backup.sh and restore-drill.sh: the settings they read from .env,
# and the two tools they share. Not `source .env` -- Compose's format allows
# unquoted spaces (TLS=...), which bash would run as a command.

env_get() { grep -E "^$1=" .env | tail -n 1 | cut -d= -f2-; }

for key in MIGRATE_DATABASE_URL BACKUP_PASSPHRASE BACKUP_S3_ENDPOINT BACKUP_S3_ACCESS_KEY \
	BACKUP_S3_SECRET_KEY BACKUP_S3_BUCKET; do
	[[ -n ${!key:-} ]] || printf -v "$key" '%s' "$(env_get "$key")"
	[[ -n ${!key} ]] || { echo "set $key in .env" >&2; exit 1; }
	export "${key?}"
done

# Secrets reach containers as `-e NAME`, inherited from this environment --
# never `-e NAME=value`, which any user on the box can read in `ps`.
export DBURL=$MIGRATE_DATABASE_URL

# rclone, configured entirely from the environment, so no credentials file
# sits on disk. NO_CHECK_BUCKET: a token scoped to one bucket cannot list
# buckets, and rclone would otherwise try to create it.
export RCLONE_CONFIG_R2_TYPE=s3 RCLONE_CONFIG_R2_PROVIDER=Cloudflare RCLONE_CONFIG_R2_NO_CHECK_BUCKET=true
export RCLONE_CONFIG_R2_ENDPOINT=$BACKUP_S3_ENDPOINT
export RCLONE_CONFIG_R2_ACCESS_KEY_ID=$BACKUP_S3_ACCESS_KEY
export RCLONE_CONFIG_R2_SECRET_ACCESS_KEY=$BACKUP_S3_SECRET_KEY
rclone() {
	docker run --rm -i --network host \
		-e RCLONE_CONFIG_R2_TYPE -e RCLONE_CONFIG_R2_PROVIDER -e RCLONE_CONFIG_R2_NO_CHECK_BUCKET \
		-e RCLONE_CONFIG_R2_ENDPOINT -e RCLONE_CONFIG_R2_ACCESS_KEY_ID -e RCLONE_CONFIG_R2_SECRET_ACCESS_KEY \
		rclone/rclone:1.68.2 "$@"
}

# The server's major version: the dump and the drill use that postgres image,
# so pg_dump is never older than the server it reads.
server_major() {
	local num
	num=$(docker run --rm --network host -e DBURL postgres:18 \
		sh -c 'exec psql "$DBURL" -XAtqc "SHOW server_version_num"')
	echo $((num / 10000))
}
