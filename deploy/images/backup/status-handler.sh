#!/bin/sh
# One HTTP request, forked by socat. Serves two endpoints that answer two
# different questions (ADR 0009):
#
#   /healthz  is the scheduler alive?          -- the Docker health check
#   /status   when did a backup last happen?   -- the River PeriodicJob polls it
#
# Collapsing them would tie a liveness probe to a 48-hour staleness window, and
# Compose does not restart on unhealthy anyway (only Swarm does), so an
# unhealthy backup container would simply sit there being unhealthy.

set -eu
. /usr/local/lib/vyn/lib.sh

respond() {
	_status="$1"
	_type="$2"
	_body="$3"
	printf 'HTTP/1.1 %s\r\n' "$_status"
	printf 'Content-Type: %s\r\n' "$_type"
	printf 'Content-Length: %s\r\n' "$(printf '%s' "$_body" | wc -c)"
	printf 'Connection: close\r\n\r\n'
	printf '%s' "$_body"
}

# Request line only; the rest of the request is irrelevant to both endpoints.
IFS=' ' read -r _method path _rest || exit 0
path="${path%%\?*}"

case "$path" in
/healthz)
	pid_file="$STATE_DIR/supercronic.pid"
	if [ -r "$pid_file" ] && kill -0 "$(cat "$pid_file")" 2>/dev/null; then
		respond "200 OK" "text/plain" "ok"
	else
		respond "503 Service Unavailable" "text/plain" "scheduler not running"
	fi
	;;

/status)
	local_file="$STATE_DIR/status-local.json"
	offsite_file="$STATE_DIR/status-offsite.json"

	# No reading at all is reported as an error rather than as an empty
	# success. A poll that errors counts as stale: the container being absent
	# or newly started with nothing behind it is precisely what the staleness
	# check exists to catch.
	if [ ! -s "$local_file" ]; then
		respond "503 Service Unavailable" "application/json" \
			'{"error":"no reading of the local repository yet"}'
		exit 0
	fi

	body="$(jq -n \
		--slurpfile local "$local_file" \
		--argjson offsite "$([ -s "$offsite_file" ] && cat "$offsite_file" || echo null)" \
		--arg now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
		--arg keep_daily "$KEEP_DAILY" \
		--arg keep_weekly "$KEEP_WEEKLY" \
		'{
		   now: $now,
		   retention: {keep_daily: ($keep_daily|tonumber), keep_weekly: ($keep_weekly|tonumber)},
		   repositories: {local: $local[0], offsite: $offsite}
		 }')"

	respond "200 OK" "application/json" "$body"
	;;

*)
	respond "404 Not Found" "text/plain" "not found"
	;;
esac
