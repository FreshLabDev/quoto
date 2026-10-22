// SPDX-License-Identifier: Apache-2.0

package db

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/FreshLabDev/quoto/migrations"
)

func TestMigrationFilesAreOrderedByVersion(t *testing.T) {
	fsys := fstest.MapFS{
		"010_later.sql":   {},
		"002_second.sql":  {},
		"001_first.sql":   {},
		"README.md":       {},
		"down/002_x.sql":  {},
		"notes/ignored.x": {},
	}
	got, err := migrationFiles(fsys)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range got {
		names = append(names, m.name)
	}
	if want := "001_first.sql 002_second.sql 010_later.sql"; strings.Join(names, " ") != want {
		t.Fatalf("got %v, want %s", names, want)
	}
}

func TestMigrationFilesRejectBadNames(t *testing.T) {
	for name, fsys := range map[string]fstest.MapFS{
		"no number":   {"initial.sql": {}},
		"zero":        {"000_zero.sql": {}},
		"duplicate":   {"001_a.sql": {}, "001_b.sql": {}},
		"bad version": {"x1_a.sql": {}},
	} {
		if _, err := migrationFiles(fsys); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestEmbeddedMigrationsParse(t *testing.T) {
	got, err := migrationFiles(migrations.FS)
	if err != nil || len(got) == 0 || got[0].name != baselineName || got[0].version != baselineVersion {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestExpectedSchemaHasNoDuplicates(t *testing.T) {
	tables := map[string]bool{}
	for _, tbl := range expectedSchema {
		if tables[tbl.table] {
			t.Errorf("table %s twice", tbl.table)
		}
		tables[tbl.table] = true
		cols := map[string]bool{}
		for _, c := range tbl.columns {
			if cols[c.name] {
				t.Errorf("%s.%s twice", tbl.table, c.name)
			}
			cols[c.name] = true
		}
	}
}
