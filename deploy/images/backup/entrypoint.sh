#!/bin/sh
# Backup sidecar: a scheduler and a status server, nothing else.
#
# Design: docs/adr/0009-forward-only-migrations-and-operational-alerting.md
# Procedures: docs/ops/runbook.md
#
# `docker compose exec backup backup-now` runs the daily job out of band. That
# path is not a convenience: a forward-only schema (ADR 0009) has no down
# migration to escape through, so an on-demand backup IS the rollback, and the
# forward-migration procedure blocks on it.

set -eu
. /usr/local/lib/vyn/lib.sh

CRONTAB="${CRONTAB:-/etc/vyn/crontab}"
STATUS_LISTEN_PORT="${STATUS_LISTEN_PORT:-8080}"

mkdir -p "$STATE_DIR"

[ -r "$CRONTAB" ] || die "no crontab at $CRONTAB"
require_file "$RESTIC_PASSWORD_FILE" "the restic repository password"

# Refuse to start against a repository that does not exist, rather than
# creating one. See assert_repo_exists in lib.sh for why this matters more than
# it looks like it should.
assert_repo_exists local

# The off-server leg is checked but not required: the laptop being off is the
# expected case, and the offsite staleness check is what judges it.
if [ -n "$RESTIC_SFTP_REPO" ]; then
	if restic_offsite cat config >/dev/null 2>&1; then
		log "off-server repository reachable"
	else
		log "off-server repository not reachable right now (this is not an error)"
	fi
else
	log "WARNING: RESTIC_SFTP_REPO is unset -- there is no off-server copy, and spec 12 asks for one"
fi

# Take an initial reading so /status has something truthful to serve before the
# first scheduled refresh, five minutes from now.
status-refresh.sh local || log "initial local status refresh failed"

log "starting supercronic with $CRONTAB"
supercronic "$CRONTAB" &
supercronic_pid=$!
echo "$supercronic_pid" >"$STATE_DIR/supercronic.pid"

cleanup() {
	rm -f "$STATE_DIR/supercronic.pid"
	kill "$supercronic_pid" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

log "serving /healthz and /status on :${STATUS_LISTEN_PORT}"
socat -T15 "TCP-LISTEN:${STATUS_LISTEN_PORT},reuseaddr,fork" \
	SYSTEM:/usr/local/bin/status-handler.sh &
socat_pid=$!

# Either process dying takes the container down, so `restart: unless-stopped`
# brings the pair back together. A half-running sidecar -- a schedule with no
# status, or a status endpoint with no schedule -- is the state most likely to
# be mistaken for a working one.
while kill -0 "$supercronic_pid" 2>/dev/null && kill -0 "$socat_pid" 2>/dev/null; do
	sleep 5
done

kill -0 "$supercronic_pid" 2>/dev/null || die "supercronic exited"
die "the status server exited"
