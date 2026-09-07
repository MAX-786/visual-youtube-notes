#!/bin/sh
# Refresh the cached view of one repository, for /status to serve.
#
#   status-refresh.sh local     -- every 5 minutes
#   status-refresh.sh offsite   -- hourly (the target is a laptop that is
#                                  usually off, and its threshold is ten days,
#                                  so hourly resolution is ample)
#
# Truth is restic's own snapshot list, not a flag a script wrote about itself:
# a flag file happily reports success for a snapshot that `forget --prune` has
# since removed, so it would be wrong exactly when it matters. See ADR 0009.
#
# Refreshing on a schedule rather than inside the HTTP handler keeps /status
# instant and keeps a poll from ever contending with a running prune for the
# repository lock.

set -eu
. /usr/local/lib/vyn/lib.sh

KIND="${1:?usage: status-refresh.sh local|offsite}"
OUT="$STATE_DIR/status-${KIND}.json"

mkdir -p "$STATE_DIR"

now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

snapshots=""
error=""

case "$KIND" in
local)
	if ! snapshots="$(restic_local snapshots --json 2>&1)"; then
		error="$snapshots"
		snapshots=""
	fi
	;;
offsite)
	if [ -z "$RESTIC_SFTP_REPO" ]; then
		error="not configured"
	elif ! snapshots="$(restic_offsite snapshots --json 2>&1)"; then
		error="$snapshots"
		snapshots=""
	fi
	;;
*)
	die "unknown repository kind '$KIND'"
	;;
esac

# A failed read leaves the previous cache in place. The snapshot timestamps it
# holds are only ever older than reality, so staleness stays conservative --
# it can raise a false alarm, never suppress a true one. `as_of` says how old
# the reading is so the poller can tell the two apart.
if [ -z "$snapshots" ]; then
	if [ -s "$OUT" ]; then
		jq --arg at "$now" --arg err "$error" \
			'.last_error = $err | .last_error_at = $at' \
			"$OUT" >"$OUT.tmp" && mv "$OUT.tmp" "$OUT"
		log "status[$KIND]: refresh failed, kept the previous reading: $error"
	else
		jq -n --arg kind "$KIND" --arg at "$now" --arg err "$error" \
			'{repository: $kind, as_of: $at, latest: {}, last_error: $err, last_error_at: $at}' \
			>"$OUT"
		log "status[$KIND]: refresh failed and there is no previous reading: $error"
	fi
	exit 0
fi

# One restic call per repository; jq reduces every snapshot to the newest time
# per tag. A snapshot carrying several tags counts for each of them.
echo "$snapshots" | jq \
	--arg kind "$KIND" \
	--arg at "$now" \
	'{
	   repository: $kind,
	   as_of: $at,
	   latest: (
	     [ .[] | . as $s | ($s.tags // [])[] | {tag: ., time: $s.time, id: $s.short_id} ]
	     | group_by(.tag)
	     | map(max_by(.time))
	     | map({(.tag): {time: .time, snapshot: .id}})
	     | add // {}
	   ),
	   snapshot_count: length,
	   last_error: null,
	   last_error_at: null
	 }' >"$OUT.tmp"

mv "$OUT.tmp" "$OUT"
log "status[$KIND]: refreshed"
