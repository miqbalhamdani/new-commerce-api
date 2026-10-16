#!/usr/bin/env bash
# Prints what the running stack is actually tuned to (P1-002): Postgres and
# Redis settings as the servers report them, and each container's limits.
set -euo pipefail
cd "$(dirname "$0")"

echo "== postgres"
if docker compose config --services | grep -qx postgres; then
	docker compose exec -T postgres psql -U postgres -d new_commerce -AtF ' = ' -c "
  SELECT name, setting || coalesce(' ' || unit, '') FROM pg_settings
  WHERE name IN ('shared_buffers', 'effective_cache_size', 'max_connections', 'work_mem',
                 'maintenance_work_mem', 'random_page_cost', 'wal_level')
  ORDER BY name"
else
	echo "managed by the provider (compose.managed-db.yaml); db-preflight.sh reports it"
fi

echo "== redis"
for k in appendonly appendfsync maxmemory maxmemory-policy; do
	docker compose exec -T redis redis-cli CONFIG GET "$k" | paste -sd= -
done

echo "== limits (cpus, memory)"
for id in $(docker compose ps -q); do
	docker inspect -f '{{.Name}} {{.HostConfig.NanoCpus}} {{.HostConfig.Memory}}' "$id" |
		awk '{ printf "%-28s %s cpu  %d MiB\n", substr($1, 2), $2 / 1e9, $3 / 1048576 }'
done
