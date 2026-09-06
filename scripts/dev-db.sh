#!/bin/sh
# Starts (or restarts) a local migrated PostgreSQL for development and
# integration tests, applies River's own schema, and prints the URL to export.
# Run from the repository root.
#
#   sh scripts/dev-db.sh
#   export VYN_TEST_DATABASE_URL=postgres://vyn:devpass@127.0.0.1:55432/vyn?sslmode=disable
set -eu
export MSYS_NO_PATHCONV=1

NAME="${VYN_DEV_DB_NAME:-vyn-dev-db}"
PORT="${VYN_DEV_DB_PORT:-55432}"
IMAGE="${PG_IMAGE:-pgvector/pgvector:pg17}"
RIVER_VERSION="$(sed -n 's#^\s*github.com/riverqueue/river v\([0-9.]*\).*#\1#p' backend/go.mod | head -1)"

docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" -p "$PORT:5432" -e POSTGRES_PASSWORD=devpass -e POSTGRES_USER=vyn -e POSTGRES_DB=vyn "$IMAGE" >/dev/null
i=0
until docker exec "$NAME" pg_isready -U vyn -d vyn >/dev/null 2>&1; do
	i=$((i + 1))
	[ "$i" -lt 60 ] || { echo "postgres did not become ready" >&2; exit 1; }
	sleep 1
done
URL="postgres://vyn:devpass@127.0.0.1:$PORT/vyn?sslmode=disable"

# River owns River's schema (ADR 0009); the CLI version is the library's.
(cd backend && go run "github.com/riverqueue/river/cmd/river@v$RIVER_VERSION" migrate-up --line main --database-url "$URL" >/dev/null)

docker cp deploy/migrations "$NAME":/mig >/dev/null
for f in deploy/migrations/*.up.sql; do
	docker exec "$NAME" psql -v ON_ERROR_STOP=1 -q -U vyn -d vyn -f "/mig/$(basename "$f")" >/dev/null
done
echo "$URL"
