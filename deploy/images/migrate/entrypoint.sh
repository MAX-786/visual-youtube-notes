#!/bin/sh
# Migration one-shot. Runs as two Compose services from one image:
#
#   entrypoint.sh river   -- River's own schema, via River's own CLI
#   entrypoint.sh app     -- our six-and-counting migrations, via golang-migrate
#
# Ordering between them is arbitrary and therefore fixed as river-then-app:
# note_stages.river_job_id is a bare bigint with no foreign key into River, so
# nothing forces a sequence. See ADR 0009.

set -eu

MODE="${1:-app}"

MIGRATIONS_PATH="${MIGRATIONS_PATH:-/migrations}"
PGHOST="${PGHOST:-postgres}"
PGPORT="${PGPORT:-5432}"
PGDATABASE="${PGDATABASE:-vyn}"
PGUSER="${PGUSER:-vyn}"
PGPASSWORD_FILE="${PGPASSWORD_FILE:-/run/secrets/postgres_password}"

log() { echo "migrate[$MODE]: $*"; }
die() { echo "migrate[$MODE]: $*" >&2; exit 1; }

[ -r "$PGPASSWORD_FILE" ] || die "cannot read password file $PGPASSWORD_FILE"

# The password is read here rather than interpolated into a Compose `command:`
# so it never appears in `docker inspect` or in this process's argv.
PGPASSWORD="$(cat "$PGPASSWORD_FILE")"
DATABASE_URL="postgres://${PGUSER}:${PGPASSWORD}@${PGHOST}:${PGPORT}/${PGDATABASE}?sslmode=disable"

# --------------------------------------------------------------------------
# River's schema
# --------------------------------------------------------------------------
if [ "$MODE" = "river" ]; then
	log "applying River's own migrations (line: main)"
	river migrate-up --line main --database-url "$DATABASE_URL"
	log "River schema up to date"
	exit 0
fi

[ "$MODE" = "app" ] || die "unknown mode '$MODE' (expected 'river' or 'app')"

# --------------------------------------------------------------------------
# Our schema
# --------------------------------------------------------------------------

migrate_cmd() {
	migrate -path="$MIGRATIONS_PATH" -database "$DATABASE_URL" "$@"
}

# Highest version this image carries. Filenames are {version}_{title}.up.sql,
# zero-padded, so a lexical sort is a numeric sort; `10#` then strips the
# padding so the shell does not read 000007 as octal.
highest_file_version() {
	set -- "$MIGRATIONS_PATH"/*.up.sql
	[ -e "$1" ] || die "no *.up.sql files under $MIGRATIONS_PATH"
	last=""
	for f do last="$f"; done
	v="$(basename "$last" | cut -d_ -f1)"
	echo $((10#$v))
}

# `migrate version` writes the database's version even when this image carries
# none of the files for it, which is exactly the comparison below needs.
#
# Two traps, both verified against migrate/migrate:v4.19.0 rather than assumed:
#   - a fresh database exits 1 with "no migration", so exit status cannot be
#     used to mean failure here;
#   - a dirty database exits *0* and prints "N (dirty)", so the dirty state must
#     be read out of the output, never inferred from the exit code.
if db_version_raw="$(migrate_cmd version 2>&1)"; then
	version_read_ok=yes
else
	version_read_ok=no
fi

case "$db_version_raw" in
*"no migration"*)
	log "fresh database, applying every migration"
	migrate_cmd up
	log "schema now at version $(highest_file_version)"
	exit 0
	;;
*"(dirty)"*)
	dirty_version="${db_version_raw%% *}"
	die "database is DIRTY at version ${dirty_version}: a previous migration failed part-way.
       Nothing is applied and the stack will not start until this is resolved by hand.
       See 'Clearing a dirty migration' in docs/ops/runbook.md."
	;;
esac

# Anything that is not exactly a number is a failure -- a refused connection, a
# bad password, a missing database -- and it must never reach the comparison
# below. "dial tcp 172.18.0.2:5432: connect: connection refused" stripped to its
# digits is a very large number, which would read as "the schema is ahead of
# this build" and let api and worker start against a database nobody migrated.
db_version="$db_version_raw"
case "${version_read_ok}:${db_version}" in
no:*) die "could not reach the database to read its version: ${db_version_raw}" ;;
yes:) die "the database reported an empty version" ;;
yes:*[!0-9]*) die "could not parse the database version: ${db_version_raw}" ;;
esac

file_version="$(highest_file_version)"

if [ "$db_version" -gt "$file_version" ]; then
	# THIS EXIT 0 IS LOAD-BEARING -- do not "fix" it into a failure.
	#
	# The schema never moves backwards (ADR 0009), so a rollback always lands
	# old code on a newer schema. That is the defined outcome of a rollback, not
	# corruption. golang-migrate disagrees: `up` here exits 1 with "no migration
	# found for version N", and because api and worker gate on this service with
	# service_completed_successfully, a non-zero exit means the rollback cannot
	# start the stack at all -- during an incident, which is the only time anyone
	# rolls back.
	#
	# Exiting 0 is licensed by exactly one rule, and is unsafe without it: every
	# migration must be compatible with the currently-running application
	# version. See the migration-authoring checklist in docs/ops/runbook.md.
	log "database is at version ${db_version}; this image carries up to ${file_version}"
	log "schema is AHEAD of this build -- expected after a rollback, nothing to apply"
	exit 0
fi

if [ "$db_version" -eq "$file_version" ]; then
	log "schema already at version ${file_version}, nothing to apply"
	exit 0
fi

log "applying migrations ${db_version} -> ${file_version}"
migrate_cmd up
log "schema now at version ${file_version}"
