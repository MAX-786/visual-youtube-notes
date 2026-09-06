// Command worker is the background processing process for Visual YouTube
// Notes: River queues, the Stage workers, and the housekeeping jobs.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/ai"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/config"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/db"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/keys"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/pipeline"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("worker exited", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.Connect(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()
	ring, err := keys.Load(cfg.MasterKeyFile)
	if err != nil {
		return err
	}
	if err := ai.CheckKeyVersions(ctx, pool, ring); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.Worker.TempDir, 0o750); err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}

	deps := &pipeline.Deps{
		Pool:      pool,
		Extractor: pipeline.NewYtDlp(cfg.Worker.YtDlpBin, cfg.Worker.YtDlpCacheDir),
		TempDir:   cfg.Worker.TempDir,
		Logger:    logger,
		AI:        ai.NewStore(pool, ring),
	}
	workers := river.NewWorkers()
	pipeline.RegisterWorkers(workers, deps)
	pipeline.RegisterPeriodicWorkers(workers, deps)

	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			pipeline.QueueDefault: {MaxWorkers: max(cfg.Worker.QueueDefaultWorkers, 1)},
			pipeline.QueueMedia:   {MaxWorkers: max(cfg.Worker.QueueMediaWorkers, 1)},
		},
		Workers:      workers,
		PeriodicJobs: pipeline.PeriodicJobs(),
		Logger:       logger,
		// Per-Stage timeouts come from each worker's Timeout(); a job stuck
		// past its timeout is rescued five minutes later, not River's
		// one-hour default, which would blow every target in spec 15.
		RescueStuckJobsAfter: pipeline.StageTimeouts[pipeline.StageTranscribing] + 5*time.Minute,
	})
	if err != nil {
		return fmt.Errorf("river client: %w", err)
	}
	deps.Client = client

	// Startup cleanup (spec 15): whatever a crashed run left behind.
	if err := pipeline.Sweep(ctx, deps, 6*time.Hour); err != nil {
		logger.Warn("startup temp sweep failed", "err", err)
	}

	health := &http.Server{
		Addr:              cfg.Worker.HealthListen,
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/healthz" {
				http.NotFound(w, r)
				return
			}
			pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := pool.Ping(pingCtx); err != nil {
				http.Error(w, `{"status":"database unreachable"}`, http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}),
	}
	go func() {
		if err := health.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("health server", "err", err)
		}
	}()

	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("start river: %w", err)
	}
	logger.Info("worker running", "default_workers", cfg.Worker.QueueDefaultWorkers, "media_workers", cfg.Worker.QueueMediaWorkers)
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = health.Shutdown(shutdownCtx)
	if err := client.Stop(shutdownCtx); err != nil {
		hard, cancelHard := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelHard()
		_ = client.StopAndCancel(hard)
	}
	return nil
}

var _ pgx.Tx // keep the driver's transaction type visible for readers of this file
