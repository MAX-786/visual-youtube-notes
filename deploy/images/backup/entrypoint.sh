#!/bin/sh
# Placeholder entrypoint.
#
# Ticket #20 decided the backup sidecar's shape: this image, derived from the
# server's own Postgres image, running its own scheduler so `docker compose up`
# brings the backup schedule with it. The scripts themselves -- the pg_dump
# pipeline into restic, the weekly `restic copy` to the SFTP repository, the
# status endpoint, and the restore drill -- are the deliverable of ticket #21.
#
# Until then this container starts, says so, and reports itself unhealthy. It
# does not exit, because a crash loop would bury the message in restart noise;
# and it does not pretend to be healthy, because a backup service that lies
# about its state is worse than one that is obviously absent.

set -eu

CRONTAB=/etc/vyn/crontab
STATUS_LISTEN="${STATUS_LISTEN:-:8080}"

if [ -f "$CRONTAB" ]; then
	echo "backup: starting supercronic with $CRONTAB"
	exec supercronic "$CRONTAB"
fi

echo "backup: NOT CONFIGURED -- no crontab at $CRONTAB."
echo "backup: the schedule and scripts are ticket #21 (operational runbook)."
echo "backup: this container is idling and will report unhealthy on $STATUS_LISTEN."

# Idle without spinning. The healthcheck curls a port nothing is listening on,
# so the service shows unhealthy, which is the accurate state.
while true; do
	sleep 3600
done
