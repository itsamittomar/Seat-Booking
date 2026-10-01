// Package store owns the Postgres connection pool and schema migrations.
package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	Pool *pgxpool.Pool
}

// Open connects with backoff for up to maxWait, so a cold-starting database
// (Neon resumes from suspend on first connection) does not kill the service.
func Open(ctx context.Context, url string, maxConns int32, maxWait time.Duration) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("store: parse url: %w", err)
	}
	cfg.MaxConns = maxConns
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: new pool: %w", err)
	}
	deadline := time.Now().Add(maxWait)
	delay := 500 * time.Millisecond
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = pool.Ping(pingCtx)
		cancel()
		if err == nil {
			return &Store{Pool: pool}, nil
		}
		if time.Now().After(deadline) {
			pool.Close()
			return nil, fmt.Errorf("store: database not reachable after %s: %w", maxWait, err)
		}
		slog.Warn("database not ready, retrying", "err", err, "retry_in", delay.String())
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if delay < 5*time.Second {
			delay *= 2
		}
	}
}

// Ping runs a real query so readiness reflects whether the DB can serve, not just whether a socket is open.
func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.Pool.QueryRow(ctx, "SELECT 1").Scan(&one)
}

func (s *Store) Close() { s.Pool.Close() }
