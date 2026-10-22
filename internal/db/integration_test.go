// SPDX-License-Identifier: Apache-2.0
//go:build integration

// These tests need a real PostgreSQL 17 and a scratch database whose name
// contains "test": they drop and recreate schema quoto and truncate core's
// tables.
//
//	QUOTO_TEST_DATABASE_URL=postgres://postgres@127.0.0.1:54329/quoto_test go test -tags=integration ./internal/db/
package db

import (
	"context"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/quoto/migrations"
)

func testURL(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("QUOTO_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set QUOTO_TEST_DATABASE_URL to run the database tests")
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.Contains(strings.TrimPrefix(u.Path, "/"), "test") {
		t.Fatalf("QUOTO_TEST_DATABASE_URL must name a disposable database containing \"test\"")
	}
	return raw
}

// admin is a pool on the default search_path, for setup the bot itself never does.
func admin(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), testURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// resetDatabase leaves core as the stub defines it, empty, and schema quoto
// absent (create = false) or present and empty (create = true).
func resetDatabase(t *testing.T, create bool) {
	t.Helper()
	ctx := context.Background()
	a := admin(t)
	stub, err := os.ReadFile("dbtest/core_stub.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(ctx, "DROP SCHEMA IF EXISTS quoto CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(ctx, string(stub)); err != nil {
		t.Fatalf("core stub: %v", err)
	}
	if _, err := a.Exec(ctx, `TRUNCATE core.person, core.chat, core.language_observation, core.language_pref,
		core.presence, core.person_name_history, core.chat_alias CASCADE`); err != nil {
		t.Fatal(err)
	}
	if create {
		if _, err := a.Exec(ctx, "CREATE SCHEMA quoto"); err != nil {
			t.Fatal(err)
		}
	}
}

func connect(t *testing.T) *Store {
	t.Helper()
	s, err := Connect(context.Background(), testURL(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// migratedStore is a store on a database migrated from scratch.
func migratedStore(t *testing.T) *Store {
	t.Helper()
	resetDatabase(t, false)
	s := connect(t)
	if err := s.Migrate(context.Background(), migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

func ledger(t *testing.T, s *Store) []int {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// catalogColumns reads quoto's columns the way adoptAlembic does.
func catalogColumns(t *testing.T, s *Store) map[string]map[string]column {
	t.Helper()
	rows, err := s.pool.Query(context.Background(),
		`SELECT table_name, column_name, udt_name, is_nullable = 'YES' FROM information_schema.columns WHERE table_schema = 'quoto'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]map[string]column{}
	for rows.Next() {
		var table string
		var c column
		if err := rows.Scan(&table, &c.name, &c.typ, &c.nullable); err != nil {
			t.Fatal(err)
		}
		if out[table] == nil {
			out[table] = map[string]column{}
		}
		out[table][c.name] = c
	}
	return out
}

// expectedSchema and the baseline must say the same thing, or adoption would
// accept or refuse the wrong databases.
func TestBaselineMatchesExpectedSchema(t *testing.T) {
	s := migratedStore(t)
	have := catalogColumns(t, s)
	delete(have, "schema_migrations")

	want := map[string]bool{}
	for _, tbl := range expectedSchema {
		want[tbl.table] = true
		cols, ok := have[tbl.table]
		if !ok {
			t.Errorf("baseline lacks table %s", tbl.table)
			continue
		}
		for _, c := range tbl.columns {
			if got, ok := cols[c.name]; !ok || got != c {
				t.Errorf("%s.%s: baseline has %+v (present %t), expectedSchema %+v", tbl.table, c.name, got, ok, c)
			}
		}
		if len(cols) != len(tbl.columns) {
			t.Errorf("%s: baseline has %d columns, expectedSchema %d", tbl.table, len(cols), len(tbl.columns))
		}
	}
	for table := range have {
		if !want[table] {
			t.Errorf("baseline creates table %s that expectedSchema does not list", table)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := migratedStore(t)
	if got := ledger(t, s); !equalInts(got, []int{1, 2}) {
		t.Fatalf("ledger %v", got)
	}
	if err := s.Migrate(context.Background(), migrations.FS); err != nil {
		t.Fatal(err)
	}
	if got := ledger(t, s); !equalInts(got, []int{1, 2}) {
		t.Fatalf("ledger after second run %v", got)
	}
}

func TestMigrateCreatesSchemaOnlyWhenMissing(t *testing.T) {
	resetDatabase(t, true) // schema exists and is empty: must not fail on CREATE SCHEMA
	s := connect(t)
	if err := s.Migrate(context.Background(), migrations.FS); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentMigrate(t *testing.T) {
	resetDatabase(t, false)
	a, b := connect(t), connect(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, s := range []*Store{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.Migrate(context.Background(), migrations.FS)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("instance %d: %v", i, err)
		}
	}
	if got := ledger(t, a); !equalInts(got, []int{1, 2}) {
		t.Fatalf("ledger %v", got)
	}
}

// pythonDatabase builds what the Python version left behind: the baseline's
// tables, no ledger, alembic_version at revision.
func pythonDatabase(t *testing.T, revision string) *Store {
	t.Helper()
	resetDatabase(t, true)
	ctx := context.Background()
	baseline, err := migrations.FS.ReadFile(baselineName)
	if err != nil {
		t.Fatal(err)
	}
	// search_path is per connection, so the whole setup runs on one.
	conn, err := admin(t).Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	for _, stmt := range []string{
		"SET search_path = quoto",
		string(baseline),
		`CREATE TABLE alembic_version (version_num varchar(32) NOT NULL PRIMARY KEY)`,
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if revision != "" {
		if _, err := conn.Exec(ctx, `INSERT INTO alembic_version VALUES ($1)`, revision); err != nil {
			t.Fatal(err)
		}
	}
	return connect(t)
}

func TestAdoptAlembicHead(t *testing.T) {
	s := pythonDatabase(t, alembicHead)
	ctx := context.Background()
	// A row that the baseline would destroy if it ran again.
	if _, err := s.pool.Exec(ctx, `SELECT core.touch('quoto', 1, NULL, 'T', NULL, NULL, -100, 'supergroup', 'G', NULL, false)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO group_settings (chat_id, is_active) VALUES (-100, true)`); err != nil {
		t.Fatal(err)
	}

	if err := s.Migrate(ctx, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if got := ledger(t, s); !equalInts(got, []int{1, 2}) {
		t.Fatalf("ledger %v", got)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM group_settings`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("group row: %d %v", n, err)
	}
	var idx int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname = 'quoto' AND indexname = 'ix_messages_chat_created'`).Scan(&idx); err != nil || idx != 1 {
		t.Fatalf("002 not applied: %d %v", idx, err)
	}
	var rev string
	if err := s.pool.QueryRow(ctx, `SELECT version_num FROM alembic_version`).Scan(&rev); err != nil || rev != alembicHead {
		t.Fatalf("alembic_version touched: %q %v", rev, err)
	}
}

func TestAdoptRefusals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		revision string
		mutate   string
		want     string
	}{
		{"other revision", "20260901_01", "", "upgrade it with the Python release"},
		{"newer revision", "20270101_01", "", "run a newer release of this bot"},
		{"missing column", alembicHead, "ALTER TABLE quotes DROP COLUMN decision_reason", "quotes.decision_reason"},
		{"retyped column", alembicHead, "ALTER TABLE messages ALTER COLUMN text TYPE varchar(20)", "messages.text"},
		{"nullability", alembicHead, "ALTER TABLE group_settings ALTER COLUMN quote_hour SET NOT NULL", "group_settings.quote_hour"},
		{"missing table", alembicHead, "DROP TABLE message_ai_scores", "message_ai_scores"},
		{"tables without revision", "", "", "no migration record"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := pythonDatabase(t, tc.revision)
			if tc.mutate != "" {
				if _, err := s.pool.Exec(context.Background(), tc.mutate); err != nil {
					t.Fatal(err)
				}
			}
			err := s.Migrate(context.Background(), migrations.FS)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want it to mention %q", err, tc.want)
			}
			if got := ledger(t, s); len(got) != 0 {
				t.Errorf("a refused start recorded %v", got)
			}
		})
	}
}

func TestExtraColumnsAreTolerated(t *testing.T) {
	s := pythonDatabase(t, alembicHead)
	if _, err := s.pool.Exec(context.Background(), `ALTER TABLE quotes ADD COLUMN spare text`); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(context.Background(), migrations.FS); err != nil {
		t.Fatal(err)
	}
}

// The family has no down migrations; these exist so CI can prove 002 reverses.
func TestUpDownUp(t *testing.T) {
	s := migratedStore(t)
	ctx := context.Background()
	down, err := os.ReadFile("../../migrations/down/002_indexes.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, string(down)); err != nil {
		t.Fatalf("down: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version = 2`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname = 'quoto' AND indexname IN ('ix_messages_chat_created', 'ix_message_ai_scores_message_db_id')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("indexes after down: %d %v", n, err)
	}
	if err := s.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("second up: %v", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname = 'quoto' AND indexname IN ('ix_messages_chat_created', 'ix_message_ai_scores_message_db_id')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("indexes after second up: %d %v", n, err)
	}
}

// The runtime role cannot create schemas or write core's tables. Run the whole
// path as it.
func TestMigrateAsRuntimeRole(t *testing.T) {
	resetDatabase(t, false)
	a := admin(t)
	ctx := context.Background()
	if _, err := a.Exec(ctx, `CREATE SCHEMA quoto AUTHORIZATION quoto_core`); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(testURL(t))
	u.User = url.User("quoto_core")
	s, err := Connect(ctx, u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate as quoto_core: %v", err)
	}
	// Writes go through core's functions only.
	if _, err := s.pool.Exec(ctx, `INSERT INTO core.chat (chat_id) VALUES (1)`); err == nil {
		t.Fatal("quoto_core wrote a core table directly")
	}
}

func TestMigrateWithoutSchemaPrivilege(t *testing.T) {
	resetDatabase(t, false)
	a := admin(t)
	ctx := context.Background()
	// quoto_core has no CREATE on the database, so a missing schema is an error
	// that says what to do.
	if _, err := a.Exec(ctx, `REVOKE CREATE ON DATABASE quoto_test FROM PUBLIC`); err != nil {
		t.Skipf("cannot revoke: %v", err)
	}
	t.Cleanup(func() { _, _ = a.Exec(ctx, `GRANT CREATE ON DATABASE quoto_test TO PUBLIC`) })
	u, _ := url.Parse(testURL(t))
	u.User = url.User("quoto_core")
	s, err := Connect(ctx, u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	err = s.Migrate(ctx, migrations.FS)
	if err == nil || !strings.Contains(err.Error(), "cannot be created") {
		t.Fatalf("error %v", err)
	}
}

// runtimeStore is a store connected as quoto_core.
func runtimeStore(t *testing.T) *Store {
	t.Helper()
	u, _ := url.Parse(testURL(t))
	u.User = url.User("quoto_core")
	s, err := Connect(context.Background(), u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// A schema owned by someone else, where quoto_core can use and write the tables
// but not create in the schema: a restart of a migrated database must not need
// CREATE (review D-1).
func TestRestartNeedsNoCreatePrivilege(t *testing.T) {
	migratedStore(t) // as the owner, schema quoto owned by the test user
	a := admin(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`GRANT USAGE ON SCHEMA quoto TO quoto_core`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA quoto TO quoto_core`,
		`GRANT USAGE ON ALL SEQUENCES IN SCHEMA quoto TO quoto_core`,
	} {
		if _, err := a.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	s := runtimeStore(t)
	if err := s.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("restart as a role with USAGE only: %v", err)
	}
	if got := ledger(t, s); !equalInts(got, []int{1, 2}) {
		t.Fatalf("ledger %v", got)
	}
}

// The first start on a database whose schema the runtime role cannot create in
// fails with a message that says what to grant.
func TestFirstStartWithoutCreatePrivilegeSaysWhatToGrant(t *testing.T) {
	pythonDatabase(t, alembicHead)
	a := admin(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`GRANT USAGE ON SCHEMA quoto TO quoto_core`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA quoto TO quoto_core`,
	} {
		if _, err := a.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	err := runtimeStore(t).Migrate(ctx, migrations.FS)
	if err == nil || !strings.Contains(err.Error(), "GRANT CREATE ON SCHEMA quoto") {
		t.Fatalf("error %v, want the grant to be named", err)
	}
}

// An index someone made by hand must not stop the start (review D-2).
func TestIndexCreatedByHandDoesNotStopMigration(t *testing.T) {
	s := pythonDatabase(t, alembicHead)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `CREATE INDEX ix_messages_chat_created ON messages (chat_id, created_at)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if got := ledger(t, s); !equalInts(got, []int{1, 2}) {
		t.Fatalf("ledger %v", got)
	}
}
