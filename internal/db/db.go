// Package db is searchy's private search-analytics store (counts/timing of
// searches and selections — never query text). Shared identity, presence and
// language live in internal/core instead. Every method is nil-safe — if the bot
// runs without a database (DATABASE_URL unset or unreachable), the Store is nil
// and all calls become harmless no-ops, so search keeps working.
package db

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

type Store struct {
	pool *pgxpool.Pool
}

// Open connects to Postgres and applies the schema. Returns (nil, nil) if url is
// empty (persistence disabled). A connect/ping failure returns an error so the
// caller can decide to continue without persistence.
func Open(ctx context.Context, url string) (*Store, error) {
	if url == "" {
		return nil, nil
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = 8
	cfg.MinConns = 1 // keep one warm so interactive /stats & language reads don't pay a cold connect
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	// Bound schema application: schema.sql also sets lock_timeout/statement_timeout,
	// but a context deadline guarantees startup can't wedge on a held lock.
	schemaCtx, scancel := context.WithTimeout(ctx, 15*time.Second)
	defer scancel()
	if _, err := pool.Exec(schemaCtx, schemaSQL); err != nil {
		pool.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

func (s *Store) ready() bool { return s != nil && s.pool != nil }
