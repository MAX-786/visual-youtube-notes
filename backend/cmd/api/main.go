// Command api is the HTTP API process for Visual YouTube Notes.
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

	"github.com/MAX-786/visual-youtube-notes/backend/internal/app"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	if len(os.Args) > 1 && os.Args[1] != "serve" {
		if err := runAdmin(os.Args[1:], logger); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(logger); err != nil {
		logger.Error("api exited", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, logger)
	if err != nil {
		return err
	}
	defer a.Close()

	server := &http.Server{
		Addr:              a.Config.Listen,
		Handler:           a.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: it would guillotine the event stream (ADR 0008).
		IdleTimeout: 5 * time.Minute,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	logger.Info("api listening", "addr", a.Config.Listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
