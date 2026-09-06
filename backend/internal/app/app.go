// Package app wires the API process: configuration, database, the contract,
// the rate limiter, and the router. cmd/api is a thin main around it so tests
// can boot the same assembly.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/audit"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/config"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/contract"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/db"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/httpapi"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/ratelimit"
)

// App is the assembled API.
type App struct {
	Config  *config.Config
	Pool    *pgxpool.Pool
	Handler http.Handler
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
	c, err := contract.Load()
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("load contract: %w", err)
	}
	recorder := audit.New(pool, logger)
	limiter := ratelimit.New(c.Classes, rateLimitSink(recorder))

	handler := httpapi.NewRouter(httpapi.Deps{
		Config:   cfg,
		Pool:     pool,
		Contract: c,
		Limiter:  limiter,
		Logger:   logger,
		Audit:    recorder,
	})
	return &App{Config: cfg, Pool: pool, Handler: handler}, nil
}

// Close releases the database pool.
func (a *App) Close() {
	if a.Pool != nil {
		a.Pool.Close()
	}
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
