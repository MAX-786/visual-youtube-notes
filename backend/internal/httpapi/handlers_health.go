package httpapi

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/apierr"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/health"
)

func toRestoreDrill(d *health.Drill) RestoreDrill {
	return RestoreDrill{
		Id: Uuid(d.ID), CompletedAt: d.CompletedAt, Repository: BackupRepository(d.Repository), SnapshotId: d.SnapshotID,
		RestoreSeconds: d.RestoreSeconds, KeyRecoveryVerified: d.KeyRecoveryVerified,
		PostgresVersion: d.PostgresVersion, PgvectorVersion: d.PgvectorVersion, Notes: d.Notes,
	}
}

// GetHealth is GET /settings/health: every system_health row with unknown
// computed from a stale checked_at, the drill history newest first, which
// repository the next drill exercises, and the Notes blocked by extractor
// breakage (ADR 0009).
func (s *Server) GetHealth(ctx context.Context, _ GetHealthRequestObject) (GetHealthResponseObject, error) {
	session := SessionFrom(ctx)
	report, err := health.BuildReport(ctx, s.deps.Pool, session.UserID, time.Now())
	if err != nil {
		return nil, apierr.Internal(err)
	}
	out := HealthReport{
		Checks:              make([]HealthCheck, 0, len(report.Checks)),
		Drills:              make([]RestoreDrill, 0, len(report.Drills)),
		NextDrillRepository: BackupRepository(report.NextDrillRepository),
		BlockedByTooling:    report.BlockedByTooling,
		GeneratedAt:         report.GeneratedAt,
	}
	for i := range report.Checks {
		c := &report.Checks[i]
		out.Checks = append(out.Checks, HealthCheck{
			CheckName: c.Name, Status: HealthStatus(c.Status), Message: c.Message, Observed: c.Observed,
			CadenceSeconds: int(c.Cadence.Seconds()), CheckedAt: c.CheckedAt,
			LastOkAt: c.LastOkAt, FirstAlarmAt: c.FirstAlarmAt, LastNotifiedAt: c.LastNotifiedAt,
		})
	}
	for i := range report.Drills {
		out.Drills = append(out.Drills, toRestoreDrill(&report.Drills[i]))
	}
	return GetHealth200JSONResponse(out), nil
}

// RecordRestoreDrill is POST /settings/health/drills: append-only, the
// measured restore time being the only RTO this project has.
func (s *Server) RecordRestoreDrill(ctx context.Context, req RecordRestoreDrillRequestObject) (RecordRestoreDrillResponseObject, error) {
	d := health.Drill{
		Repository:          string(req.Body.Repository),
		SnapshotID:          strings.TrimSpace(req.Body.SnapshotId),
		RestoreSeconds:      req.Body.RestoreSeconds,
		KeyRecoveryVerified: req.Body.KeyRecoveryVerified,
		PostgresVersion:     strings.TrimSpace(req.Body.PostgresVersion),
		PgvectorVersion:     strings.TrimSpace(req.Body.PgvectorVersion),
	}
	if req.Body.Notes != nil {
		if notes := strings.TrimSpace(*req.Body.Notes); notes != "" {
			d.Notes = &notes
		}
	}
	rec, err := health.RecordDrill(ctx, s.deps.Pool, d, time.Now())
	if err != nil {
		if errors.Is(err, health.ErrInvalidDrill) {
			return nil, apierr.New(apierr.InvalidRequest, "A drill needs a repository, a snapshot id, a positive restore time and both versions.")
		}
		return nil, apierr.Internal(err)
	}
	return RecordRestoreDrill201JSONResponse(toRestoreDrill(rec)), nil
}
