#!/bin/sh
# Applies every migration and every .test.sql sibling in a throwaway
# pgvector/pg17 container, then reverses the whole down chain and checks that
# nothing of ours survives. Run from the repository root; CI runs it on every
# push. Verified in a container rather than reasoned about, because that is
# how tickets #19, #21, #29 and #30 each caught a real defect.
set -eu

# Git Bash on Windows rewrites arguments that start with a slash into Windows
# paths; the container paths below must reach docker untouched.
export MSYS_NO_PATHCONV=1

IMAGE="${PG_IMAGE:-pgvector/pgvector:pg17}"
NAME="vyn-migrations-$$"

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM

docker run -d --name "$NAME" -e POSTGRES_PASSWORD=x -e POSTGRES_USER=vyn -e POSTGRES_DB=vyn "$IMAGE" >/dev/null
i=0
until docker exec "$NAME" pg_isready -U vyn -d vyn >/dev/null 2>&1; do
	i=$((i + 1))
	[ "$i" -lt 60 ] || { echo "postgres did not become ready" >&2; exit 1; }
	sleep 1
done
docker cp deploy/migrations "$NAME":/mig >/dev/null

run_sql() {
	docker exec "$NAME" psql -v ON_ERROR_STOP=1 -q -U vyn -d vyn -f "/mig/$1" >/dev/null
}

count=0
for f in deploy/migrations/*.up.sql; do
	run_sql "$(basename "$f")"
	count=$((count + 1))
done
echo "up: $count migrations applied"

for f in deploy/migrations/*.test.sql; do
	run_sql "$(basename "$f")"
	echo "test ok: $(basename "$f")"
done

for f in $(ls deploy/migrations/*.down.sql | sort -r); do
	run_sql "$(basename "$f")"
done
echo "down: $count migrations reversed"

left="$(docker exec "$NAME" psql -U vyn -d vyn -tAc "select count(*) from pg_class c join pg_namespace n on n.oid = c.relnamespace where n.nspname = 'public' and c.relkind in ('r','v','S')")"
enums="$(docker exec "$NAME" psql -U vyn -d vyn -tAc "select count(*) from pg_type t join pg_namespace n on n.oid = t.typnamespace where n.nspname = 'public' and t.typtype = 'e'")"
if [ "$left" != "0" ] || [ "$enums" != "0" ]; then
	echo "down chain left $left relations and $enums enums behind" >&2
	exit 1
fi
echo "migrations: up, tests and down are all clean"
