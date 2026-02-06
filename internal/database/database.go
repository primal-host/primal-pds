package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps a pgx connection pool with application-level helpers.
type DB struct {
	Pool *pgxpool.Pool
}

// Open connects to PostgreSQL using the given connection string, verifies
// the connection with a ping, and bootstraps the schema. It returns a
// ready-to-use DB instance.
//
// The connection pool is configured for a management-oriented workload
// (moderate concurrency, long-lived connections).
func Open(ctx context.Context, connString string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("database: parse config: %w", err)
	}

	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("database: connect: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database: ping: %w", err)
	}

	// Bootstrap schema. The statements are idempotent (IF NOT EXISTS)
	// so this is safe to run on every startup.
	if _, err := pool.Exec(ctx, Schema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database: bootstrap schema: %w", err)
	}

	return &DB{Pool: pool}, nil
}

// Close shuts down the connection pool. Call this during graceful shutdown.
func (db *DB) Close() {
	db.Pool.Close()
}
