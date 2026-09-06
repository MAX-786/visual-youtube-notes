// Package testdb connects integration tests to a migrated PostgreSQL named by
// VYN_TEST_DATABASE_URL, and skips them when it is absent. CI provides one;
// locally, scripts/dev-db.sh starts it.
package testdb

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool returns a pool for the test database, or skips the test.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("VYN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("VYN_TEST_DATABASE_URL not set; integration test skipped")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Reset empties every table the tests write, so each test starts from a clean
// Owner-less database. The seeded tables (prices, defaults, index state) are
// left alone.
func Reset(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		TRUNCATE auth_security_events, push_subscriptions, pairing_requests,
		         session_refresh_tokens, session_access_tokens, device_sessions,
		         ai_usage_events, ai_cost_settings, ai_role_configs,
		         note_mutations, sync_events, sync_cursors,
		         search_embeddings, search_children, stage_attempts, note_stages,
		         note_diagram_layouts, note_takeaways, note_tags, note_revisions,
		         transcript_chunks, transcripts, notes, tags, topics, sources, users
		RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("reset test database: %v", err)
	}
}
