// SPDX-License-Identifier: Apache-2.0

// Package db is Quoto's PostgreSQL access: the pool, the migration runner and
// the stores. Domain tables live in schema quoto on the shared core database;
// Telegram identity, chats and language are core's and are reached only through
// its functions (see core.go).
//
// SQL sits next to the method that runs it. Every statement names core objects
// with their schema (core.touch, core.chat), because the connection's
// search_path is quoto alone.
package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// Schema is the schema Quoto owns.
const Schema = "quoto"

// Store is the connection pool and every store method.
type Store struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	// now is the clock the stores stamp rows with; tests replace it.
	now func() time.Time
}

// Connect opens the pool and checks that the database answers. It does not
// migrate; see Migrate.
func Connect(ctx context.Context, databaseURL string, log *slog.Logger) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// The parse error can quote the URL, password included.
		return nil, errors.New("parse DATABASE_URL: not a valid postgres URL")
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = Schema
	// Small on purpose: 8 groups run at once, updates are handled one by one per
	// poll batch, and a Postgres connection costs the server about 10 MB.
	cfg.MaxConns = 12
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Store{pool: pool, log: log, now: time.Now}, nil
}

// Close releases the pool.
func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

// Ping is the database half of /healthz.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// utcNow is the clock for row timestamps.
func (s *Store) utcNow() time.Time { return s.now().UTC() }

// dbtx is what a statement runs on: the pool, or a transaction.
type dbtx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// inTx runs fn in a transaction: committed if fn returns nil, rolled back
// otherwise.
func (s *Store) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return err
	}
	return tx.Commit(ctx)
}

// SQLSTATE codes the stores react to.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
	pgNotNullViolation    = "23502"
)

func hasCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

// nullStr maps the domain's empty string to NULL.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullInt64 maps 0 to NULL; Telegram ids, sizes and dimensions are never 0.
func nullInt64(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullFloat(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

// str, i64, i and f read a nullable column scanned into a pointer.
func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func i64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func i(p *int32) int {
	if p == nil {
		return 0
	}
	return int(*p)
}

func f(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// trimmed is Python's value.strip() with an empty result turned into NULL, the
// way core_client prepared every string it handed to core.touch.
func trimmed(s string) any { return nullStr(domain.PyStrip(s)) }

// cut truncates to n code points, like Python's s[:n].
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
