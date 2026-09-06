// Package db opens the PostgreSQL pool both processes share.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/config"
)

// Connect opens a pool and proves it with a ping, so a bad password fails the
// boot rather than the first request.
func Connect(ctx context.Context, database config.Database) (*pgxpool.Pool, error) {
	connString, err := database.URL()
	if err != nil {
		return nil, err
	}
	poolConfig, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("parse database config: %w", err)
	}
	// Compose caps PostgreSQL at 60 connections (ADR 0008); the API and the
	// worker each keep well inside a fair share of it.
	poolConfig.MaxConns = 12
	poolConfig.MinConns = 1
	poolConfig.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}
