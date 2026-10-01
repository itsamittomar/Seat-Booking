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
	// health is a separate one-connection pool for readiness checks. Under load the main pool
	// has a queue of requests waiting for a connection; a readiness probe that joined that queue
	// would time out, the platform would mark the instance unhealthy and restart it at peak.
	health *pgxpool.Pool
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
			health, herr := newHealthPool(ctx, url)
			if herr != nil {
				pool.Close()
				return nil, herr
			}
			return &Store{Pool: pool, health: health}, nil
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

func newHealthPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("store: parse url: %w", err)
	}
	cfg.MaxConns = 1
	cfg.MinConns = 1
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	return pgxpool.NewWithConfig(ctx, cfg)
}

// Ping runs a real query on the dedicated health connection, so readiness reflects whether the
// database can serve, independent of how busy the request pool is.
func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.health.QueryRow(ctx, "SELECT 1").Scan(&one)
}

func (s *Store) Close() {
	s.health.Close()
	s.Pool.Close()
}
