#!/usr/bin/env bash
# Rolls a release out with no request dropped (P1-001):
#   deploy.sh <tag>       e.g. deploy.sh sha-1a2b3c4 (CI tags every merge to main)
#
# Migrations first, then one API replica at a time: a stopping replica drains
# (its /healthz turns 503 and Caddy routes around it) while the other serves,
# and the next one waits until the new one is healthy. Then web and the worker.
#
# On a developer machine: SKIP_PULL=1, with COMPOSE_FILE and COMPOSE_ENV_FILES
# set as README.md says, and images built locally.
set -euo pipefail
cd "$(dirname "$0")"

tag=${1:?usage: deploy.sh <image tag>}
if [[ $tag != local ]]; then
	export API_IMAGE=ghcr.io/miqbalhamdani/new-commerce-api:$tag
	export WEB_IMAGE=ghcr.io/miqbalhamdani/new-commerce-web:$tag
fi

if [[ ${SKIP_PULL:-} != 1 ]]; then
	docker compose pull --quiet api-1 web
fi

# postgres only when this stack runs it: compose.managed-db.yaml takes it out
# of the service list.
if docker compose config --services | grep -qx postgres; then
	docker compose up -d --wait postgres redis
else
	docker compose up -d --wait redis
fi
docker compose run --rm migrate

healthy() {
	local id
	id=$(docker compose ps -q "$1")
	[[ -n $id && $(docker inspect -f '{{.State.Health.Status}}' "$id") == healthy ]]
}

for svc in api-1 api-2; do
	echo "rolling $svc"
	docker compose up -d --no-deps --force-recreate "$svc"
	for _ in $(seq 60); do healthy "$svc" && break; sleep 1; done
	healthy "$svc" || { echo "$svc did not become healthy" >&2; exit 1; }
	# One more of Caddy's 1 s health checks, so it is back in the pool
	# before its partner leaves it.
	sleep 2
done

docker compose up -d --no-deps web worker caddy
echo "deployed $tag"
