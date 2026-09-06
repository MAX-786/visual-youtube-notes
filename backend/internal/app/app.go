// Package app wires the API process: configuration, database, the master
// key ring, the contract, the rate limiter, authentication, and the router.
// cmd/api is a thin main around it so tests can boot the same assembly.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/audit"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/auth"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/config"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/contract"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/db"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/httpapi"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/keys"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/notes"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/pipeline"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/ratelimit"
	vsync "github.com/MAX-786/visual-youtube-notes/backend/internal/sync"
)

// App is the assembled API.
type App struct {
	Config  *config.Config
	Pool    *pgxpool.Pool
	Keys    *keys.Ring
	Auth    *auth.Service
	Sync    *vsync.Broker
	Handler http.Handler
	stop    context.CancelFunc
}

// New boots the API. Every failure here is a configuration or connectivity
// problem that should stop the process rather than surface per request.
func New(ctx context.Context, logger *slog.Logger) (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	pool, err := db.Connect(ctx, cfg.Database)
	if err != nil {
		return nil, err
	}
	ring, err := keys.Load(cfg.MasterKeyFile)
	if err != nil {
		pool.Close()
		return nil, err
	}
	if err := checkKeyVersions(ctx, pool, ring); err != nil {
		pool.Close()
		return nil, err
	}
	c, err := contract.Load()
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("load contract: %w", err)
	}
	recorder := audit.New(pool, logger)
	limiter := ratelimit.New(c.Classes, rateLimitSink(recorder))
	authService := auth.New(pool, auth.NewKeyring(ring), recorder)
	broker := vsync.NewBroker(pool, logger)
	brokerCtx, stop := context.WithCancel(context.Background())
	go broker.Run(brokerCtx)

	// An insert-only River client: the API enqueues the first Stage and
	// cancels runs; it never works jobs.
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Logger: logger})
	if err != nil {
		stop()
		pool.Close()
		return nil, fmt.Errorf("river client: %w", err)
	}

	handler := httpapi.NewRouter(httpapi.Deps{
		Config:      cfg,
		Pool:        pool,
		Contract:    c,
		Limiter:     limiter,
		Logger:      logger,
		Audit:       recorder,
		Auth:        authService,
		Sync:        broker,
		Submissions: pipeline.NewService(pool, riverClient, logger),
		Notes:       notes.New(pool),
		Authenticate: func(ctx context.Context, _ httpapi.Transport, credential string) *httpapi.Session {
			s := authService.Authenticate(ctx, credential)
			if s == nil {
				return nil
			}
			return &httpapi.Session{
				ID:              s.ID,
				UserID:          s.UserID,
				Kind:            s.Kind,
				ExtensionID:     s.ExtensionID,
				CSRFHash:        s.CSRFHash,
				AccessExpiresAt: s.AccessExpiresAt,
			}
		},
	})
	return &App{Config: cfg, Pool: pool, Keys: ring, Auth: authService, Sync: broker, Handler: handler, stop: stop}, nil
}

// Close stops the listener and releases the database pool.
func (a *App) Close() {
	if a.stop != nil {
		a.stop()
	}
	if a.Pool != nil {
		a.Pool.Close()
	}
}

// checkKeyVersions refuses to boot when a provider key was sealed under a
// master key version the file no longer holds (ADR 0010). Booting anyway
// would turn a configuration problem into a per-Note decrypt failure that
// reads like a provider fault.
func checkKeyVersions(ctx context.Context, pool *pgxpool.Pool, ring *keys.Ring) error {
	rows, err := pool.Query(ctx, `SELECT DISTINCT key_version FROM ai_role_configs WHERE key_version IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("read key versions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return err
		}
		if _, ok := ring.ByVersion(version); !ok {
			return fmt.Errorf("provider keys are sealed under master key version %d, which the key file does not hold; restore it before starting", version)
		}
	}
	return rows.Err()
}

// rateLimitSink records the transition into a limited state as a
// rate_limited Security event with the class in reason_code (ADR 0016).
func rateLimitSink(recorder *audit.Recorder) ratelimit.Sink {
	return func(ctx context.Context, t ratelimit.Transition) {
		ev := audit.Event{Type: audit.RateLimited, SourceIP: t.SourceIP, ReasonCode: t.Class}
		if t.SessionID != "" {
			if id, err := uuid.Parse(t.SessionID); err == nil {
				ev.DeviceSessionID = &id
			}
		}
		recorder.Record(ctx, ev)
	}
}
