// SPDX-License-Identifier: Apache-2.0

// Package dbtest gives integration tests a migrated database to work on. It is
// imported by tests only.
//
// The tests need a real PostgreSQL 17 and a scratch database whose name contains
// "test": every call drops and recreates schema quoto and empties core's tables.
// QUOTO_TEST_DATABASE_URL names it; without it the test is skipped.
package dbtest

import (
	"context"
	_ "embed"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/quoto/internal/db"
	"github.com/FreshLabDev/quoto/migrations"
)

//go:embed core_stub.sql
var coreStub string

// URL is the scratch database's URL, or it skips the test.
func URL(t testing.TB) string {
	t.Helper()
	raw := os.Getenv("QUOTO_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set QUOTO_TEST_DATABASE_URL to run the database tests")
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.Contains(strings.TrimPrefix(u.Path, "/"), "test") {
		t.Fatal("QUOTO_TEST_DATABASE_URL must name a disposable database containing \"test\"")
	}
	return raw
}

// New resets the database to a fresh migrated Quoto schema on top of the core
// stub and returns a store on it. The store is closed when the test ends.
func New(t testing.TB) *db.Store {
	t.Helper()
	ctx := context.Background()
	raw := URL(t)

	admin, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, stmt := range []string{
		"DROP SCHEMA IF EXISTS quoto CASCADE",
		coreStub,
		`TRUNCATE core.person, core.chat, core.language_observation, core.language_pref,
			core.presence, core.person_name_history, core.chat_alias CASCADE`,
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("reset database: %v", err)
		}
	}

	store, err := db.Connect(ctx, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

// Exec runs setup SQL on the store's connection (search_path quoto), failing the
// test on error.
func Exec(t testing.TB, s *db.Store, sql string, args ...any) {
	t.Helper()
	if _, err := s.Pool().Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}
