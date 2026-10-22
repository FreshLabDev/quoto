// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// insufficientPrivilege is SQLSTATE 42501.
const insufficientPrivilege = "42501"

const (
	// migrateLockKey serialises starts: two instances of the bot (a rolling
	// restart, a test stack next to a real one) never migrate at once.
	migrateLockKey = 4242424501

	// alembicHead is the last revision the Python version applied. A database at
	// this revision is the same schema as migrations/001_baseline.sql.
	alembicHead = "20261018_01"

	baselineVersion = 1
	baselineName    = "001_baseline.sql"
)

// Migrate applies every migration in fsys that the ledger lacks, oldest first,
// one transaction per file. Files are named NNN_description.sql; the number is
// the version.
//
// A database the Python version migrated with Alembic is recognised and adopted
// rather than migrated: see adoptAlembic.
func (s *Store) Migrate(ctx context.Context, fsys fs.FS) (err error) {
	files, err := migrationFiles(fsys)
	if err != nil {
		return err
	}

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrateLockKey); err != nil {
		return fmt.Errorf("migrate: take lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, uerr := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, migrateLockKey); uerr != nil {
			// A session-level lock outlives the connection's return to the pool, so
			// a connection that cannot release it is closed instead.
			_ = conn.Hijack().Close(unlockCtx)
			err = errors.Join(err, fmt.Errorf("migrate: release lock: %w", uerr))
		}
	}()

	if err := ensureSchema(ctx, conn); err != nil {
		return err
	}
	if err := ensureLedger(ctx, conn); err != nil {
		return err
	}
	if err := adoptAlembic(ctx, conn, s.utcNow()); err != nil {
		return err
	}

	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return err
	}
	for _, m := range files {
		if applied[m.version] {
			continue
		}
		sql, err := fs.ReadFile(fsys, m.name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", m.name, err)
		}
		if err := applyOne(ctx, conn, m, string(sql)); err != nil {
			return fmt.Errorf("apply migration %s: %w%s", m.name, err, ownershipHint(err))
		}
		s.log.Info("migration applied", "migration", m.name)
	}
	return nil
}

func applyOne(ctx context.Context, conn *pgxpool.Conn, m migration, sql string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, sql); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, m.version, m.name); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return err
	}
	return tx.Commit(ctx)
}

type migration struct {
	version int
	name    string
}

// migrationFiles lists the *.sql files at the top of fsys in version order.
func migrationFiles(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	var out []migration
	seen := map[int]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, _ := strings.Cut(name, "_")
		version, err := strconv.Atoi(prefix)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid migration file name %q: want NNN_description.sql", name)
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations %s and %s share version %d", other, name, version)
		}
		seen[version] = name
		out = append(out, migration{version: version, name: name})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].version < out[b].version })
	return out, nil
}

func appliedVersions(ctx context.Context, conn *pgxpool.Conn) (map[int]bool, error) {
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read migration ledger: %w", err)
	}
	defer rows.Close()
	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// ensureLedger creates schema_migrations when it is missing. PostgreSQL checks
// the CREATE privilege on the schema before it looks at IF NOT EXISTS, so
// running the statement on every start would make a role with only USAGE fail
// on a database that is long since migrated. The table is looked up first.
func ensureLedger(ctx context.Context, conn *pgxpool.Conn) error {
	var regclass *string
	if err := conn.QueryRow(ctx, `SELECT to_regclass($1)::text`, Schema+".schema_migrations").Scan(&regclass); err != nil {
		return fmt.Errorf("look for the migration ledger: %w", err)
	}
	if regclass != nil {
		return nil
	}
	if _, err := conn.Exec(ctx, `
		CREATE TABLE schema_migrations (
			version integer PRIMARY KEY,
			name text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create migration ledger: %w%s", err, ownershipHint(err))
	}
	return nil
}

// ownershipHint says what to change when a migration failed for lack of
// privileges. The Python version needed CREATE on schema quoto and ownership of
// its tables too (Alembic altered them), so a database it ran on has both; one
// provisioned by hand may not.
func ownershipHint(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != insufficientPrivilege {
		return ""
	}
	return fmt.Sprintf(" (the database role needs CREATE on schema %s on the first start, and to own the tables the migration changes; "+
		"as the schema owner: GRANT CREATE ON SCHEMA %s TO <role>, or run the migration files by hand and add them to schema_migrations)",
		Schema, Schema)
}

// ensureSchema creates schema quoto when it is missing. The runtime role
// usually cannot (it has no CREATE on the database), so the error names the
// fix: whoever provisions the database creates the schema once.
func ensureSchema(ctx context.Context, conn *pgxpool.Conn) error {
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)`, Schema).Scan(&exists); err != nil {
		return fmt.Errorf("check schema %s: %w", Schema, err)
	}
	if exists {
		return nil
	}
	if _, err := conn.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{Schema}.Sanitize()); err != nil {
		return fmt.Errorf("schema %s does not exist and cannot be created (create it once as a database owner): %w", Schema, err)
	}
	return nil
}

// adoptAlembic records the baseline as applied on a database the Python
// version created, so the baseline does not run over existing tables.
//
// It acts only while the ledger is empty. The database must hold exactly the
// Alembic head revision and every table and column of the baseline; anything
// else stops the start with an error, because guessing here would either skip a
// migration the schema needs or run DDL over data.
//
//   - no alembic_version and none of the tables: a fresh database, the baseline
//     runs;
//   - alembic_version at the head revision: adopted after the catalog check;
//   - any other revision, or tables without a revision record: refused.
func adoptAlembic(ctx context.Context, conn *pgxpool.Conn, now time.Time) error {
	var ledgerRows int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&ledgerRows); err != nil {
		return fmt.Errorf("read migration ledger: %w", err)
	}
	if ledgerRows > 0 {
		return nil
	}

	present, err := presentTables(ctx, conn)
	if err != nil {
		return err
	}
	revisions, hasAlembic, err := alembicRevisions(ctx, conn)
	if err != nil {
		return err
	}

	switch {
	case hasAlembic && len(revisions) == 1 && revisions[0] == alembicHead:
		if err := verifyBaselineSchema(ctx, conn); err != nil {
			return fmt.Errorf("database is at Alembic revision %s but its schema does not match the baseline: %w", alembicHead, err)
		}
		_, err := conn.Exec(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES ($1, $2, $3)`,
			baselineVersion, baselineName, now)
		if err != nil {
			return fmt.Errorf("record adopted baseline: %w", err)
		}
		return nil
	case hasAlembic && len(revisions) == 1 && revisions[0] > alembicHead:
		return fmt.Errorf("database is at Alembic revision %s, newer than the %s this version knows: run a newer release of this bot",
			revisions[0], alembicHead)
	case hasAlembic && len(revisions) > 0:
		return fmt.Errorf("database is at Alembic revision %s, this version expects %s: upgrade it with the Python release first",
			strings.Join(revisions, ", "), alembicHead)
	case len(present) > 0:
		return fmt.Errorf("schema %s already has tables (%s) but no migration record: restore the record or the database",
			Schema, strings.Join(present, ", "))
	}
	return nil // a fresh database
}

// presentTables lists the baseline's tables that exist in schema quoto.
func presentTables(ctx context.Context, conn *pgxpool.Conn) ([]string, error) {
	rows, err := conn.Query(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema = $1`, Schema)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var present []string
	for _, t := range expectedSchema {
		if have[t.table] {
			present = append(present, t.table)
		}
	}
	return present, nil
}

func alembicRevisions(ctx context.Context, conn *pgxpool.Conn) (revisions []string, exists bool, err error) {
	var regclass *string
	if err := conn.QueryRow(ctx, `SELECT to_regclass($1)::text`, Schema+".alembic_version").Scan(&regclass); err != nil {
		return nil, false, fmt.Errorf("look for alembic_version: %w", err)
	}
	if regclass == nil {
		return nil, false, nil
	}
	rows, err := conn.Query(ctx, `SELECT version_num FROM alembic_version ORDER BY version_num`)
	if err != nil {
		return nil, true, fmt.Errorf("read alembic_version: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, true, err
		}
		revisions = append(revisions, v)
	}
	return revisions, true, rows.Err()
}

// verifyBaselineSchema checks every table and column of expectedSchema through
// the catalog. Extra columns are fine (a column the Go version no longer reads
// stays until a late migration drops it); missing or retyped ones are not.
func verifyBaselineSchema(ctx context.Context, conn *pgxpool.Conn) error {
	rows, err := conn.Query(ctx, `
		SELECT table_name, column_name, udt_name, is_nullable = 'YES'
		FROM information_schema.columns WHERE table_schema = $1`, Schema)
	if err != nil {
		return fmt.Errorf("read catalog: %w", err)
	}
	defer rows.Close()
	have := map[string]map[string]column{}
	for rows.Next() {
		var table string
		var c column
		if err := rows.Scan(&table, &c.name, &c.typ, &c.nullable); err != nil {
			return err
		}
		if have[table] == nil {
			have[table] = map[string]column{}
		}
		have[table][c.name] = c
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range expectedSchema {
		cols, ok := have[t.table]
		if !ok {
			return fmt.Errorf("table %s is missing", t.table)
		}
		for _, want := range t.columns {
			got, ok := cols[want.name]
			switch {
			case !ok:
				return fmt.Errorf("column %s.%s is missing", t.table, want.name)
			case got.typ != want.typ:
				return fmt.Errorf("column %s.%s has type %s, want %s", t.table, want.name, got.typ, want.typ)
			case got.nullable != want.nullable:
				return fmt.Errorf("column %s.%s nullability differs: nullable=%t, want %t", t.table, want.name, got.nullable, want.nullable)
			}
		}
	}
	return nil
}
