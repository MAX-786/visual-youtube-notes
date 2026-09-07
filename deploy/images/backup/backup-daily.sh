#!/bin/sh
# Daily backup into the local restic repository.
#
# Three snapshots, three tags, because they are recovered at different moments:
#   db       -- the dump itself
#   globals  -- roles, which are not in a pg_dump
#   keys     -- the master key and VAPID key, age-encrypted to the Owner
#
# Also runs as the manual pre-migration backup (`backup-now`), which is the only
# escape a forward-only schema has from a bad migration. See ADR 0009.

set -eu
. /usr/local/lib/vyn/lib.sh

run() {
	require_file "$RESTIC_PASSWORD_FILE" "the restic repository password"
	require_file "$MASTER_KEY_FILE" "the application master key"
	assert_repo_exists local

	[ -n "$BACKUP_RECOVERY_RECIPIENT" ] || die "BACKUP_RECOVERY_RECIPIENT is unset.
       Spec 12 requires the master key backed up under an owner-held recovery
       secret, so a database backup without it is not the backup that was asked
       for. Refusing rather than silently backing up half of what matters."

	mkdir -p "$WORK_DIR"
	chmod 700 "$WORK_DIR"
	trap 'rm -rf "$WORK_DIR"' EXIT INT TERM

	# ------------------------------------------------------------------
	# The database
	# ------------------------------------------------------------------
	# --compress=none is deliberate and is the whole reason restic earns its
	# place here: content-defined chunking can only deduplicate consecutive
	# dumps if the bytes are stable where the data is stable, and stream
	# compression shifts every byte after the first change. restic compresses
	# the chunks itself (repository format 2).
	#
	# --stdin-from-command rather than a plain pipe: restic reads the command's
	# exit code and refuses to write a snapshot if pg_dump failed. A plain
	# `pg_dump | restic --stdin` masks the error and stores a truncated dump,
	# which is a backup that exists and cannot be restored.
	log "dumping ${PGDATABASE} into the local repository"
	restic_local backup \
		--stdin-from-command --stdin-filename db.dump \
		--tag db \
		-- pg_dump --format=custom --compress=none "$PGDATABASE"

	# ------------------------------------------------------------------
	# Roles
	# ------------------------------------------------------------------
	# "To export global objects that are common to all databases in a cluster
	# (such as roles and tablespaces), use pg_dumpall." The stack has one app
	# role that Compose recreates anyway, but the file costs nothing and a
	# restore that has to guess at role names is a restore that stalls.
	log "dumping globals"
	pg_dumpall --globals-only >"$WORK_DIR/globals.sql"
	restic_local backup --tag globals "$WORK_DIR/globals.sql"

	# ------------------------------------------------------------------
	# Keys
	# ------------------------------------------------------------------
	# Encrypted to a recipient whose private identity has never been on this
	# host, so the key material rides inside the repositories -- and therefore
	# to the laptop each week -- while staying opaque to anyone holding only the
	# restic password.
	#
	# Not backed up here, deliberately: the restic password itself and the age
	# recovery identity. Both are needed to OPEN this backup, so storing them
	# inside it would be circular. They live only in the Owner's password
	# manager. postgres_password is regenerable and is reset on restore.
	log "wrapping key material for the Owner recovery identity"
	key_paths="${MASTER_KEY_FILE#/}"
	if [ -r "$VAPID_KEY_FILE" ]; then
		key_paths="$key_paths ${VAPID_KEY_FILE#/}"
	else
		log "WARNING: no VAPID key at $VAPID_KEY_FILE; push subscriptions will not survive a restore"
	fi
	# Deliberate word splitting: $key_paths is one or two paths we built above.
	# shellcheck disable=SC2086
	tar -cf - -C / $key_paths |
		age -r "$BACKUP_RECOVERY_RECIPIENT" -o "$WORK_DIR/keys.tar.age"
	restic_local backup --tag keys "$WORK_DIR/keys.tar.age"

	# ------------------------------------------------------------------
	# Retention and integrity
	# ------------------------------------------------------------------
	# Tags are forgotten independently so a policy change to one cannot quietly
	# expire another. Spec 12's deletion window is exactly these numbers: do not
	# add --keep-monthly without amending the spec.
	log "applying retention (keep-daily ${KEEP_DAILY})"
	for tag in db globals keys; do
		restic_local forget --tag "$tag" --keep-daily "$KEEP_DAILY" --prune
	done

	# A subset each night rather than --read-data every night: a full re-read of
	# every pack is hours of disk on a box that is also serving the app, and
	# 10% nightly covers the repository over a retention period anyway.
	log "verifying a subset of the repository"
	restic_local check --read-data-subset=10%

	log "daily backup complete"
}

with_lock run
