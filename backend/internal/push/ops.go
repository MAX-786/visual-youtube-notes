package push

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Ops is the API's side of operational alerts (ADR 0014): it evaluates the
// two conditions the worker cannot report on itself, writes them as
// system_health rows so suppression survives a restart, and sends one
// alert per check on the transition into alarm and then at most once a day.
type Ops struct {
	pool   *pgxpool.Pool
	sender *Sender
	log    *slog.Logger
	// Now is replaceable in tests.
	Now func() time.Time
}

// NewOps builds the evaluator; a nil sender evaluates without sending.
func NewOps(pool *pgxpool.Pool, sender *Sender, log *slog.Logger) *Ops {
	return &Ops{pool: pool, sender: sender, log: log, Now: time.Now}
}

// The checks the API owns, and their cadence (how often Run rewrites them).
const (
	CheckWorkerStale     = "worker_stale"
	CheckRiverLeaderLost = "river_leader_lost"
	opsCadence           = 60 * time.Second
	alertRepeat          = 24 * time.Hour
	// A worker row is stale once three of its cadences have passed (ADR 0009).
	staleMultiple = 3
)

// Run evaluates every minute until the context ends.
func (o *Ops) Run(ctx context.Context) {
	ticker := time.NewTicker(opsCadence)
	defer ticker.Stop()
	for {
		if err := o.Evaluate(ctx); err != nil && ctx.Err() == nil {
			o.log.Warn("operational alert evaluation failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Evaluate is one pass: upsert the API's rows, then alert on every row in
// alarm whose suppression window has passed.
func (o *Ops) Evaluate(ctx context.Context) error {
	now := o.Now()
	if err := o.evaluateWorkerStale(ctx, now); err != nil {
		return err
	}
	if err := o.evaluateLeader(ctx, now); err != nil {
		return err
	}
	return o.alert(ctx, now)
}

// evaluateWorkerStale reads the freshest worker-written row; three cadences
// without a rewrite means nothing is being processed. With no worker row at
// all there is nothing to judge, and the row is left unwritten.
func (o *Ops) evaluateWorkerStale(ctx context.Context, now time.Time) error {
	var checkedAt time.Time
	var cadence int
	err := o.pool.QueryRow(ctx, `
		SELECT checked_at, cadence_seconds FROM system_health
		 WHERE check_name NOT IN ($1, $2) ORDER BY checked_at DESC LIMIT 1`, CheckWorkerStale, CheckRiverLeaderLost).Scan(&checkedAt, &cadence)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	age := now.Sub(checkedAt)
	stale := age > time.Duration(staleMultiple*cadence)*time.Second
	observed := map[string]any{"minutes": int(age.Minutes()), "last_check_at": checkedAt.UTC().Format(time.RFC3339)}
	message := "The worker wrote its last health check " + age.Truncate(time.Minute).String() + " ago."
	if !stale {
		message = "The worker is writing health checks on time."
	}
	return o.upsert(ctx, CheckWorkerStale, stale, message, observed, now)
}

// evaluateLeader: River's leader lease past its expiry means the periodic
// jobs are not being scheduled. A lease that never existed is the worker
// never having started, which worker_stale reports.
func (o *Ops) evaluateLeader(ctx context.Context, now time.Time) error {
	var expiresAt time.Time
	err := o.pool.QueryRow(ctx, `SELECT expires_at FROM river_leader WHERE name = 'default'`).Scan(&expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	lost := now.After(expiresAt.Add(opsCadence))
	observed := map[string]any{"lease_expires_at": expiresAt.UTC().Format(time.RFC3339)}
	message := "River's leader lease is current."
	if lost {
		message = "River's leader lease expired at " + expiresAt.UTC().Format(time.RFC3339) + "; scheduled work is not running."
	}
	return o.upsert(ctx, CheckRiverLeaderLost, lost, message, observed, now)
}

// upsert writes the check, keeping first_alarm_at across evaluations while
// the alarm persists and clearing it on recovery.
func (o *Ops) upsert(ctx context.Context, check string, alarm bool, message string, observed map[string]any, now time.Time) error {
	raw, err := json.Marshal(observed)
	if err != nil {
		return err
	}
	status := "ok"
	if alarm {
		status = "alarm"
	}
	_, err = o.pool.Exec(ctx, `
		INSERT INTO system_health (check_name, status, message, observed, cadence_seconds, checked_at, last_ok_at, first_alarm_at)
		VALUES ($1, $2::text::health_status, $3, $4::jsonb, $5::int, $6::timestamptz,
		        CASE WHEN $2::text = 'ok' THEN $6::timestamptz END,
		        CASE WHEN $2::text = 'alarm' THEN $6::timestamptz END)
		ON CONFLICT (check_name) DO UPDATE
		   SET status = EXCLUDED.status, message = EXCLUDED.message, observed = EXCLUDED.observed, checked_at = EXCLUDED.checked_at,
		       last_ok_at = CASE WHEN EXCLUDED.status = 'ok' THEN EXCLUDED.checked_at ELSE system_health.last_ok_at END,
		       first_alarm_at = CASE WHEN EXCLUDED.status = 'alarm' THEN coalesce(system_health.first_alarm_at, EXCLUDED.checked_at) ELSE NULL END,
		       last_notified_at = CASE WHEN EXCLUDED.status = 'alarm' THEN system_health.last_notified_at ELSE NULL END`,
		check, status, message, raw, int(opsCadence.Seconds()), now)
	return err
}

// alert sends for every check in alarm not notified within a day, to every
// Owner with ops notifications on, and stamps last_notified_at. Fires on
// the transition into alarm and then at most once per 24 hours.
func (o *Ops) alert(ctx context.Context, now time.Time) error {
	rows, err := o.pool.Query(ctx, `
		SELECT check_name, observed FROM system_health
		 WHERE status = 'alarm' AND (last_notified_at IS NULL OR last_notified_at < $1)`, now.Add(-alertRepeat))
	if err != nil {
		return err
	}
	type due struct {
		check    string
		observed map[string]any
	}
	var dues []due
	for rows.Next() {
		var d due
		var raw []byte
		if err := rows.Scan(&d.check, &raw); err != nil {
			rows.Close()
			return err
		}
		_ = json.Unmarshal(raw, &d.observed)
		dues = append(dues, d)
	}
	rows.Close()
	if len(dues) == 0 {
		return nil
	}
	users, err := o.owners(ctx)
	if err != nil {
		return err
	}
	for _, d := range dues {
		m := OpsMessage(d.check, d.observed)
		if o.sender != nil {
			for _, userID := range users {
				if _, err := o.sender.Fanout(ctx, o.pool, userID, CategoryOps, m, alertRepeat, UrgencyHigh); err != nil {
					o.log.Warn("operational alert not fully delivered", "check", d.check, "err", err)
				}
			}
		}
		if _, err := o.pool.Exec(ctx, `UPDATE system_health SET last_notified_at = $2 WHERE check_name = $1`, d.check, now); err != nil {
			return err
		}
		o.log.Info("operational alert sent", "check", d.check)
	}
	return nil
}

func (o *Ops) owners(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := o.pool.Query(ctx, `SELECT id FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
