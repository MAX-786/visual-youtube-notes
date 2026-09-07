// Package health is system_health and the restore drill history (ADR 0009):
// the rows the worker writes about backups, disk and the extractor, the
// rows the API writes about the worker, the reader that reports a stale row
// as unknown rather than as its frozen stored status, and the drill log
// that answers which repository is due next.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Status is a check's stored status; Unknown is never stored, the reader
// computes it from a checked_at older than three cadences.
type Status string

// The stored statuses, and the one the reader computes.
const (
	StatusOK      Status = "ok"
	StatusWarn    Status = "warn"
	StatusAlarm   Status = "alarm"
	StatusUnknown Status = "unknown"
)

// StaleMultiple is how many cadences may pass before a row is unknown.
const StaleMultiple = 3

// Repositories the drill alternates between (ADR 0009).
const (
	RepositoryLocal   = "local"
	RepositoryOffsite = "offsite"
)

// ErrInvalidDrill is a drill record the table would refuse.
var ErrInvalidDrill = errors.New("health: invalid restore drill")

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Check is one system_health row as the API reports it.
type Check struct {
	Name           string
	Status         Status
	Message        *string
	Observed       map[string]any
	Cadence        time.Duration
	CheckedAt      time.Time
	LastOkAt       *time.Time
	FirstAlarmAt   *time.Time
	LastNotifiedAt *time.Time
}

// Upsert writes one check. first_alarm_at is kept across writes while the
// alarm persists and cleared on recovery, so a notification's suppression
// window survives a restart; last_notified_at likewise survives an alarm
// and clears otherwise; last_ok_at moves only on an ok.
func Upsert(ctx context.Context, q querier, name string, status Status, message string, observed map[string]any, cadence time.Duration, now time.Time) error {
	if observed == nil {
		observed = map[string]any{}
	}
	raw, err := json.Marshal(observed)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		INSERT INTO system_health (check_name, status, message, observed, cadence_seconds, checked_at, last_ok_at, first_alarm_at)
		VALUES ($1, $2::text::health_status, $3, $4::jsonb, $5::int, $6::timestamptz,
		        CASE WHEN $2::text = 'ok' THEN $6::timestamptz END,
		        CASE WHEN $2::text = 'alarm' THEN $6::timestamptz END)
		ON CONFLICT (check_name) DO UPDATE
		   SET status = EXCLUDED.status, message = EXCLUDED.message, observed = EXCLUDED.observed,
		       cadence_seconds = EXCLUDED.cadence_seconds, checked_at = EXCLUDED.checked_at,
		       last_ok_at = CASE WHEN EXCLUDED.status = 'ok' THEN EXCLUDED.checked_at ELSE system_health.last_ok_at END,
		       first_alarm_at = CASE WHEN EXCLUDED.status = 'alarm' THEN coalesce(system_health.first_alarm_at, EXCLUDED.checked_at) ELSE NULL END,
		       last_notified_at = CASE WHEN EXCLUDED.status = 'alarm' THEN system_health.last_notified_at ELSE NULL END`,
		name, string(status), message, raw, int(cadence.Seconds()), now)
	return err
}

// Read is every row, by name, with unknown computed for a checked_at older
// than three times the row's own cadence: a frozen ok is not an ok.
func Read(ctx context.Context, q querier, now time.Time) ([]Check, error) {
	rows, err := q.Query(ctx, `
		SELECT check_name, status::text, message, observed, cadence_seconds, checked_at, last_ok_at, first_alarm_at, last_notified_at
		  FROM system_health ORDER BY check_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Check{}
	for rows.Next() {
		var c Check
		var status string
		var observed []byte
		var cadence int
		if err := rows.Scan(&c.Name, &status, &c.Message, &observed, &cadence, &c.CheckedAt, &c.LastOkAt, &c.FirstAlarmAt, &c.LastNotifiedAt); err != nil {
			return nil, err
		}
		c.Status = Status(status)
		c.Cadence = time.Duration(cadence) * time.Second
		if now.Sub(c.CheckedAt) > StaleMultiple*c.Cadence {
			c.Status = StatusUnknown
		}
		c.Observed = map[string]any{}
		if len(observed) > 0 {
			_ = json.Unmarshal(observed, &c.Observed)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Drill is one completed restore drill (ADR 0009): which repository it
// proved, and the measured restore time, the only RTO this project has.
type Drill struct {
	ID                  uuid.UUID
	CompletedAt         time.Time
	Repository          string
	SnapshotID          string
	RestoreSeconds      int
	KeyRecoveryVerified bool
	PostgresVersion     string
	PgvectorVersion     string
	Notes               *string
}

// Drills is the history, newest first.
func Drills(ctx context.Context, q querier) ([]Drill, error) {
	rows, err := q.Query(ctx, `
		SELECT id, completed_at, repository::text, snapshot_id, restore_seconds, key_recovery_verified, postgres_version, pgvector_version, notes
		  FROM ops_restore_drills ORDER BY completed_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Drill{}
	for rows.Next() {
		var d Drill
		if err := rows.Scan(&d.ID, &d.CompletedAt, &d.Repository, &d.SnapshotID, &d.RestoreSeconds, &d.KeyRecoveryVerified, &d.PostgresVersion, &d.PgvectorVersion, &d.Notes); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RecordDrill appends one drill. Append-only: a drill is a fact about the
// past, never edited.
func RecordDrill(ctx context.Context, q querier, d Drill, now time.Time) (*Drill, error) {
	if (d.Repository != RepositoryLocal && d.Repository != RepositoryOffsite) || d.SnapshotID == "" || d.RestoreSeconds <= 0 || d.PostgresVersion == "" || d.PgvectorVersion == "" {
		return nil, ErrInvalidDrill
	}
	out := d
	out.CompletedAt = now
	err := q.QueryRow(ctx, `
		INSERT INTO ops_restore_drills (completed_at, repository, snapshot_id, restore_seconds, key_recovery_verified, postgres_version, pgvector_version, notes)
		VALUES ($1, $2::text::backup_repository, $3, $4, $5, $6, $7, $8) RETURNING id, completed_at`,
		now, d.Repository, d.SnapshotID, d.RestoreSeconds, d.KeyRecoveryVerified, d.PostgresVersion, d.PgvectorVersion, d.Notes).
		Scan(&out.ID, &out.CompletedAt)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// NextRepository is the one the newest drill did not exercise; the first
// drill is local (ADR 0009).
func NextRepository(newestFirst []Drill) string {
	if len(newestFirst) == 0 || newestFirst[0].Repository == RepositoryOffsite {
		return RepositoryLocal
	}
	return RepositoryOffsite
}

// BlockedByTooling counts the Owner's Notes whose current run failed with
// extractor breakage, through the view the runbook's retry selects on.
func BlockedByTooling(ctx context.Context, q querier, userID uuid.UUID) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM notes_blocked_by_tooling WHERE user_id = $1`, userID).Scan(&n)
	return n, err
}

// Report is Settings → Health.
type Report struct {
	Checks              []Check
	Drills              []Drill
	NextDrillRepository string
	BlockedByTooling    int
	GeneratedAt         time.Time
}

// BuildReport assembles the report for one Owner at now.
func BuildReport(ctx context.Context, q querier, userID uuid.UUID, now time.Time) (*Report, error) {
	checks, err := Read(ctx, q, now)
	if err != nil {
		return nil, err
	}
	drills, err := Drills(ctx, q)
	if err != nil {
		return nil, err
	}
	blocked, err := BlockedByTooling(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	return &Report{Checks: checks, Drills: drills, NextDrillRepository: NextRepository(drills), BlockedByTooling: blocked, GeneratedAt: now.UTC()}, nil
}
