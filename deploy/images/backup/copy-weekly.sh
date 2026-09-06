#!/bin/sh
# Weekly copy of the newest snapshots to the off-server repository.
#
# Scheduled Sunday and then retried every following night until it succeeds,
# because the target is a laptop that is not always on. `restic copy` skips
# snapshots the destination already holds, so a retry after a success is a
# cheap no-op rather than a duplicate.

set -eu
. /usr/local/lib/vyn/lib.sh

run() {
	if [ -z "$RESTIC_SFTP_REPO" ]; then
		log "no off-server repository configured, nothing to copy"
		exit 0
	fi

	require_file "$RESTIC_PASSWORD_FILE" "the local repository password"
	require_file "$RESTIC_SFTP_PASSWORD_FILE" "the off-server repository password"
	require_file "$RESTIC_SFTP_KEY" "the off-server SSH identity"
	assert_repo_exists local

	# The laptop being off is the expected case, not an incident. It is not
	# reported as a failure here; the offsite staleness check owns that
	# judgement, and it waits ten days before complaining. See ADR 0009.
	if ! restic_offsite cat config >/dev/null 2>&1; then
		log "off-server repository unreachable, will retry tonight"
		exit 0
	fi

	# The two repositories carry different passwords, so a compromise of this
	# host's secrets does not hand over the laptop copy once its password is
	# rotated. restic notes the cost: a copy between differently-keyed
	# repositories re-reads and re-writes the whole snapshot rather than moving
	# packs.
	log "copying newest snapshots to the off-server repository"
	restic_offsite copy \
		--from-repo "$RESTIC_LOCAL_REPO" \
		--from-password-file "$RESTIC_PASSWORD_FILE" \
		--tag db --tag globals --tag keys \
		latest

	log "applying off-server retention (keep-weekly ${KEEP_WEEKLY})"
	for tag in db globals keys; do
		restic_offsite forget --tag "$tag" --keep-weekly "$KEEP_WEEKLY" --prune
	done

	# Structure only. --read-data over SFTP to a laptop would pull the whole
	# repository across the link every week; the drill is what proves this copy
	# actually restores, and it does so twice a year by alternation.
	log "verifying off-server repository structure"
	restic_offsite check

	log "weekly copy complete"
}

with_lock run
