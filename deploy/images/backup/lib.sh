#!/bin/sh
# Shared settings and helpers for the backup sidecar. Sourced, never executed.
#
# Design and reasoning: docs/adr/0009-forward-only-migrations-and-operational-alerting.md
# Procedures: docs/ops/runbook.md

set -eu

RESTIC_LOCAL_REPO="${RESTIC_LOCAL_REPO:-/var/lib/restic}"
RESTIC_SFTP_REPO="${RESTIC_SFTP_REPO:-}"
RESTIC_PASSWORD_FILE="${RESTIC_PASSWORD_FILE:-/run/secrets/restic_password}"
RESTIC_SFTP_PASSWORD_FILE="${RESTIC_SFTP_PASSWORD_FILE:-/run/secrets/restic_sftp_password}"
RESTIC_SFTP_KEY="${RESTIC_SFTP_KEY:-/run/secrets/restic_sftp_key}"

MASTER_KEY_FILE="${MASTER_KEY_FILE:-/run/secrets/master_key}"
VAPID_KEY_FILE="${VAPID_KEY_FILE:-/run/secrets/vapid_private_key}"

PGHOST="${PGHOST:-postgres}"
PGDATABASE="${PGDATABASE:-vyn}"
PGUSER="${PGUSER:-vyn}"
PGPASSWORD_FILE="${PGPASSWORD_FILE:-/run/secrets/postgres_password}"
export PGHOST PGDATABASE PGUSER

# PGPASSFILE is not usable here: libpq wants a pgpass-format line
# (host:port:db:user:password) and the Compose secret is a bare password.
# Exporting PGPASSWORD from inside the script keeps it out of the container's
# declared environment, so it does not appear in `docker inspect`.
if [ -r "$PGPASSWORD_FILE" ]; then
	# Strip the newline and any carriage return; initdb strips both.
	PGPASSWORD="$(tr -d '\r\n' < "$PGPASSWORD_FILE")"
	export PGPASSWORD
fi

# A *public* age recipient, so it is environment rather than a Compose secret.
# ADR 0008 keeps secrets in files because environment leaks into `docker
# inspect` and into every child process; a leaked public key is a non-event.
BACKUP_RECOVERY_RECIPIENT="${BACKUP_RECOVERY_RECIPIENT:-}"

KEEP_DAILY="${KEEP_DAILY:-7}"
KEEP_WEEKLY="${KEEP_WEEKLY:-4}"

STATE_DIR="${STATE_DIR:-/var/lib/vyn-backup}"
LOCK_FILE="${LOCK_FILE:-$STATE_DIR/backup.lock}"
WORK_DIR="${WORK_DIR:-/tmp/vyn-backup}"

log() { echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) backup: $*"; }
die() {
	echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) backup: FATAL: $*" >&2
	exit 1
}

require_file() {
	[ -r "$1" ] || die "cannot read $2 at $1"
}

# restic is told the repository explicitly on every call rather than through an
# exported RESTIC_REPOSITORY, because this script talks to two repositories with
# two different passwords and an inherited default is how the wrong one gets
# written to.
restic_local() {
	restic --repo "$RESTIC_LOCAL_REPO" --password-file "$RESTIC_PASSWORD_FILE" "$@"
}

restic_offsite() {
	[ -n "$RESTIC_SFTP_REPO" ] || die "RESTIC_SFTP_REPO is not set"
	restic --repo "$RESTIC_SFTP_REPO" --password-file "$RESTIC_SFTP_PASSWORD_FILE" \
		-o sftp.args="-i $RESTIC_SFTP_KEY -o StrictHostKeyChecking=yes -o UserKnownHostsFile=$STATE_DIR/known_hosts" \
		"$@"
}

# A repository is never created implicitly. If the local volume is lost, an
# auto-initialising script would create an empty repository, the next backup
# would succeed, staleness would clear, and the dashboard would be green over
# seven days of vanished history. `restic init` is a one-time runbook step; a
# missing repository is a loud failure. See ADR 0009.
assert_repo_exists() {
	_kind="$1"
	case "$_kind" in
	local) restic_local cat config >/dev/null 2>&1 ;;
	offsite) restic_offsite cat config >/dev/null 2>&1 ;;
	*) die "unknown repository kind '$_kind'" ;;
	esac || die "the $_kind restic repository is missing or unreadable.
       This script will NOT create one: an empty repository would report healthy
       while holding nothing. See 'Initialising the backup repositories' in
       docs/ops/runbook.md."
}

# Serialises the daily job, the weekly copy and any manual run against each
# other. restic locks its own repository, but two jobs racing would still
# produce a confusing pair of failures rather than one clean wait.
with_lock() {
	mkdir -p "$STATE_DIR"
	exec 9>"$LOCK_FILE"
	flock -w "${LOCK_WAIT_SECONDS:-1800}" 9 ||
		die "another backup run held the lock for longer than ${LOCK_WAIT_SECONDS:-1800}s"
	"$@"
}
