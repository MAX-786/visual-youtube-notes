package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The checks the worker owns (docs/design/notification-copy.md).
const (
	CheckBackupLocal       = "backup_local"
	CheckBackupOffsite     = "backup_offsite"
	CheckDiskFree          = "disk_free"
	CheckExtractorBreakage = "extractor_breakage"
)

// backupTags are the three snapshots one daily backup produces; a
// repository is fresh only when all three are (backup-daily.sh).
var backupTags = []string{"db", "globals", "keys"}

// Worker is the worker's side of system_health (ADR 0009): backup staleness
// read from the sidecar's /status, which reads it from restic's own snapshot
// list; free space on the volumes this process can see; and the count of
// Notes blocked by extractor breakage. It runs on the worker every cadence,
// leader-elected through River, and the API's worker_stale check watches
// this one, since a dead worker would otherwise freeze every row at ok.
type Worker struct {
	Pool   *pgxpool.Pool
	Client *http.Client
	// StatusURL is the sidecar's /status; StaleLocal and StaleOffsite are
	// the two thresholds, 48 hours and ten days by default.
	StatusURL    string
	StaleLocal   time.Duration
	StaleOffsite time.Duration
	// The disk alarm fires below max(DiskMinFreeBytes, DiskMinFreePercent)
	// free on the first of DiskPaths; the rest are reported for attribution.
	DiskMinFreeBytes   int64
	DiskMinFreePercent float64
	DiskPaths          []string
	// Cadence is how often Run is scheduled; it is written with every row so
	// the reader can judge staleness.
	Cadence time.Duration
	Log     *slog.Logger
	// Now is replaceable in tests.
	Now func() time.Time
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Worker) client() *http.Client {
	if w.Client != nil {
		return w.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// Run is one pass over every check. Each check writes its own row even
// when another fails, and the errors come back joined.
func (w *Worker) Run(ctx context.Context) error {
	now := w.now()
	return errors.Join(w.checkBackups(ctx, now), w.checkDisk(ctx, now), w.checkExtractor(ctx, now))
}

// statusDoc is the sidecar's /status body (deploy/images/backup).
type statusDoc struct {
	Now          string                 `json:"now"`
	Repositories map[string]*repoStatus `json:"repositories"`
}

type repoStatus struct {
	AsOf   string `json:"as_of"`
	Latest map[string]struct {
		Time     time.Time `json:"time"`
		Snapshot string    `json:"snapshot"`
	} `json:"latest"`
	SnapshotCount int     `json:"snapshot_count"`
	LastError     *string `json:"last_error"`
	LastErrorAt   *string `json:"last_error_at"`
}

// checkBackups polls /status once and judges both repositories from the
// snapshot timestamps it carries. A poll that errors counts as stale for
// both, never as "skip this round": the sidecar being gone is precisely the
// case the check exists for (ADR 0009).
func (w *Worker) checkBackups(ctx context.Context, now time.Time) error {
	doc, err := w.fetchStatus(ctx)
	if err != nil {
		message := "The backup sidecar's status could not be read: " + err.Error()
		observed := map[string]any{"error": err.Error()}
		return errors.Join(
			Upsert(ctx, w.Pool, CheckBackupLocal, StatusAlarm, message, observed, w.Cadence, now),
			Upsert(ctx, w.Pool, CheckBackupOffsite, StatusAlarm, message, observed, w.Cadence, now),
		)
	}
	return errors.Join(
		w.judgeRepository(ctx, now, CheckBackupLocal, "local", doc.Repositories["local"], w.StaleLocal),
		w.judgeRepository(ctx, now, CheckBackupOffsite, "off-server", doc.Repositories["offsite"], w.StaleOffsite),
	)
}

func (w *Worker) fetchStatus(ctx context.Context) (*statusDoc, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.StatusURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := w.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var doc statusDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("unreadable status body: %w", err)
	}
	return &doc, nil
}

func (w *Worker) judgeRepository(ctx context.Context, now time.Time, check, label string, repo *repoStatus, threshold time.Duration) error {
	if repo == nil {
		return Upsert(ctx, w.Pool, check, StatusAlarm,
			"There is no reading of the "+label+" repository yet.",
			map[string]any{"error": "no reading"}, w.Cadence, now)
	}
	observed := map[string]any{"as_of": repo.AsOf, "snapshot_count": repo.SnapshotCount}
	if repo.LastError != nil && *repo.LastError != "" {
		observed["last_error"] = *repo.LastError
	}
	var newest time.Time
	var missing []string
	for _, tag := range backupTags {
		latest, ok := repo.Latest[tag]
		if !ok || latest.Time.IsZero() {
			missing = append(missing, tag)
			continue
		}
		if newest.IsZero() || latest.Time.Before(newest) {
			newest = latest.Time
		}
		if tag == "db" {
			observed["snapshot"] = latest.Snapshot
		}
	}
	if len(missing) > 0 {
		observed["missing_tags"] = missing
		message := "The " + label + " repository has no " + strings.Join(missing, ", ") + " snapshot."
		if repo.LastError != nil && *repo.LastError != "" {
			message += " Last error: " + *repo.LastError
		}
		return Upsert(ctx, w.Pool, check, StatusAlarm, message, observed, w.Cadence, now)
	}
	age := now.Sub(newest)
	days := math.Round(age.Hours()/24*10) / 10
	observed["days"] = days
	observed["latest_at"] = newest.UTC().Format(time.RFC3339)
	if age > threshold {
		message := fmt.Sprintf("No fresh %s snapshot in %.1f days; the newest complete set is from %s.", label, days, newest.UTC().Format("2 Jan 2006 15:04 UTC"))
		if repo.LastError != nil && *repo.LastError != "" {
			message += " Last error: " + *repo.LastError
		}
		return Upsert(ctx, w.Pool, check, StatusAlarm, message, observed, w.Cadence, now)
	}
	return Upsert(ctx, w.Pool, check, StatusOK,
		fmt.Sprintf("The newest complete %s set is %s old.", label, age.Truncate(time.Minute)),
		observed, w.Cadence, now)
}

// checkDisk measures every path and judges the first: alarm below
// max(DiskMinFreeBytes, DiskMinFreePercent) free, chosen to arrive before
// the transcribing Stage's own preflight starts failing (ADR 0009).
func (w *Worker) checkDisk(ctx context.Context, now time.Time) error {
	if len(w.DiskPaths) == 0 {
		return nil
	}
	const gib = 1024 * 1024 * 1024
	volumes := map[string]any{}
	var judged *diskUsage
	var firstErr error
	for i, path := range w.DiskPaths {
		u, err := usage(path)
		if err != nil {
			volumes[path] = map[string]any{"error": err.Error()}
			if i == 0 {
				firstErr = err
			}
			continue
		}
		volumes[path] = map[string]any{
			"gib_free":     math.Round(float64(u.free)/gib*10) / 10,
			"gib_total":    math.Round(float64(u.total)/gib*10) / 10,
			"percent_free": math.Round(u.percentFree()*10) / 10,
		}
		if i == 0 {
			judged = &u
		}
	}
	observed := map[string]any{"path": w.DiskPaths[0], "volumes": volumes}
	if judged == nil {
		return Upsert(ctx, w.Pool, CheckDiskFree, StatusWarn, "Free space could not be measured: "+firstErr.Error(), observed, w.Cadence, now)
	}
	observed["gib_free"] = math.Round(float64(judged.free)/gib*10) / 10
	observed["percent_free"] = math.Round(judged.percentFree()*10) / 10
	floor := w.DiskMinFreeBytes
	if byPercent := int64(w.DiskMinFreePercent / 100 * float64(judged.total)); byPercent > floor {
		floor = byPercent
	}
	observed["floor_gib"] = math.Round(float64(floor)/gib*10) / 10
	if judged.free < floor {
		return Upsert(ctx, w.Pool, CheckDiskFree, StatusAlarm,
			fmt.Sprintf("%.1f GiB free on %s, below the %.1f GiB floor. Clear space before jobs start failing.", float64(judged.free)/gib, w.DiskPaths[0], float64(floor)/gib),
			observed, w.Cadence, now)
	}
	return Upsert(ctx, w.Pool, CheckDiskFree, StatusOK,
		fmt.Sprintf("%.1f GiB free on %s (%.0f%%).", float64(judged.free)/gib, w.DiskPaths[0], judged.percentFree()),
		observed, w.Cadence, now)
}

type diskUsage struct{ free, total int64 }

func (u diskUsage) percentFree() float64 {
	if u.total <= 0 {
		return 0
	}
	return float64(u.free) / float64(u.total) * 100
}

// checkExtractor counts Notes blocked by extractor breakage through the
// runbook's view; any at all is the signal that yt-dlp needs updating.
func (w *Worker) checkExtractor(ctx context.Context, now time.Time) error {
	var blocked int
	if err := w.Pool.QueryRow(ctx, `SELECT count(*) FROM notes_blocked_by_tooling`).Scan(&blocked); err != nil {
		return err
	}
	observed := map[string]any{"notes": blocked}
	if blocked > 0 {
		noun := "notes are"
		if blocked == 1 {
			noun = "note is"
		}
		return Upsert(ctx, w.Pool, CheckExtractorBreakage, StatusAlarm,
			fmt.Sprintf("%d %s blocked until yt-dlp is updated.", blocked, noun), observed, w.Cadence, now)
	}
	return Upsert(ctx, w.Pool, CheckExtractorBreakage, StatusOK, "yt-dlp is extracting normally.", observed, w.Cadence, now)
}
