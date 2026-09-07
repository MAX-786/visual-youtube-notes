#!/usr/bin/env bash
# Roll the host forward to one image tag, the runbook's Forward migration
# procedure as a script (docs/ops/runbook.md). Run on the host from anywhere:
#
#   APP_VERSION=2026.09.07-2b48ae7a bash ~/vyn/deploy/deploy.sh
#
# The same command with an older tag is the runbook's Rollback: the schema
# stays ahead of the code and the migrate one-shot reports that and exits 0
# (ADR 0009). CI copies this file to ~/vyn/deploy/deploy.sh on every deploy.
set -euo pipefail

: "${APP_VERSION:?set APP_VERSION to the image tag to run}"
cd "${VYN_DEPLOY_DIR:-$HOME/vyn/deploy}"

previous="$(grep '^APP_VERSION=' .env | cut -d= -f2)"
echo "==> ${previous:-none} -> $APP_VERSION"

echo "==> Pre-migration backup (a forward-only schema has no other way back)"
docker compose exec -T backup backup-now </dev/null | tail -n 1

echo "==> Pull"
sed -i "s/^APP_VERSION=.*/APP_VERSION=$APP_VERSION/" .env
docker compose pull --quiet </dev/null

echo "==> Up (the migration one-shots gate the application)"
if ! docker compose up -d --remove-orphans </dev/null; then
	docker compose logs --no-log-prefix --tail 40 river-migrate migrate || true
	exit 1
fi
for svc in river-migrate migrate; do
	state="$(docker inspect --format '{{.State.Status}} {{.State.ExitCode}}' "visual-youtube-notes-$svc-1" 2>/dev/null || echo 'none 1')"
	if [ "$state" != "exited 0" ]; then
		echo "$svc: $state" >&2
		docker compose logs --no-log-prefix --tail 40 "$svc" || true
		exit 1
	fi
done
docker compose logs --no-log-prefix migrate | tail -n 1

echo "==> Health"
for attempt in $(seq 1 40); do
	api="$(docker inspect --format '{{.State.Health.Status}}' visual-youtube-notes-api-1 2>/dev/null || echo none)"
	worker="$(docker inspect --format '{{.State.Health.Status}}' visual-youtube-notes-worker-1 2>/dev/null || echo none)"
	caddy="$(docker inspect --format '{{.State.Health.Status}}' visual-youtube-notes-caddy-1 2>/dev/null || echo none)"
	# Through nginx and Caddy, the API's own unauthenticated envelope.
	code="$(curl -sk -o /dev/null -w '%{http_code}' --max-time 10 --resolve yt-track.mkhismkh.com:443:127.0.0.1 https://yt-track.mkhismkh.com/api/auth/session || echo 000)"
	if [ "$api" = healthy ] && [ "$worker" = healthy ] && [ "$caddy" = healthy ] && [ "$code" = 401 ]; then
		echo "healthy: api=$api worker=$worker caddy=$caddy session=$code"
		break
	fi
	if [ "$attempt" = 40 ]; then
		echo "not healthy after 200s: api=$api worker=$worker caddy=$caddy session=$code" >&2
		docker compose ps
		docker compose logs --no-log-prefix --tail 30 api worker caddy || true
		exit 1
	fi
	sleep 5
done

docker image prune -f >/dev/null </dev/null
echo "==> Deployed $APP_VERSION (was ${previous:-none})"
