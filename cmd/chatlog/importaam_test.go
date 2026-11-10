// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// memoryFile writes a file shaped like the one import-aam reads: two wings, a drawer of two chunks, facts with and without a drawer.
func memoryFile(t *testing.T, teams int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "memory.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE drawers (team_id TEXT, id TEXT, wing TEXT, room TEXT, source_file TEXT DEFAULT '', chunk_index INTEGER DEFAULT 0, content TEXT, entities TEXT DEFAULT '', parent_id TEXT DEFAULT '', filed_at TEXT, content_date TEXT DEFAULT '', agent TEXT DEFAULT '', topic TEXT DEFAULT '', embedded_at TEXT, valid_to TEXT DEFAULT '', superseded_by TEXT DEFAULT '', ended_reason TEXT DEFAULT '', ended_at TEXT DEFAULT '', content_key TEXT DEFAULT '')`,
		`CREATE TABLE kg_entities (team_id TEXT, id TEXT, name TEXT, created_at TEXT)`,
		`CREATE TABLE kg_triples (team_id TEXT, id TEXT, subject TEXT, predicate TEXT, object TEXT, valid_from TEXT DEFAULT '', valid_to TEXT DEFAULT '', source_file TEXT DEFAULT '', source_drawer_id TEXT DEFAULT '', extracted_at TEXT, ended_reason TEXT DEFAULT '')`,
		`INSERT INTO drawers(team_id, id, wing, room, content, filed_at) VALUES('t1','a','wing_webshop','decisions','use plain css for the forms','2026-08-01T10:00:00Z')`,
		`INSERT INTO drawers(team_id, id, wing, room, content, filed_at) VALUES('t1','b','wing_billing','facts','invoices are numbered per year','2026-08-02T10:00:00Z')`,
		`INSERT INTO drawers(team_id, id, wing, room, chunk_index, content, parent_id, filed_at) VALUES('t1','b-1','wing_billing','facts',1,'and credit notes follow them','b','2026-08-02T10:00:00Z')`,
		`INSERT INTO kg_entities VALUES('t1','web','webshop','x'),('t1','css','css','x'),('t1','inv','invoices','x')`,
		`INSERT INTO kg_triples(team_id, id, subject, predicate, object, source_drawer_id, extracted_at) VALUES('t1','f1','web','uses','css','a','2026-08-01')`,
		`INSERT INTO kg_triples(team_id, id, subject, predicate, object, extracted_at) VALUES('t1','f2','inv','are','css','2026-08-01')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if teams == 2 {
		if _, err := db.Exec(`INSERT INTO drawers(team_id, id, wing, room, content, filed_at) VALUES('t2','z','wing_x','r','other team','2026-08-01T10:00:00Z')`); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestImportAAMStoresArchivedRowsAndAgainChangesNothing(t *testing.T) {
	h := newHome(t)
	file := memoryFile(t, 1)
	out, err := h.run("", "import-aam", file)
	if err != nil || !strings.Contains(out, "imported 3 documents of team t1") || !strings.Contains(out, "2 drawers (1 of them several chunks joined, 0 ended), 1 facts appended to their drawers, and 1 facts in 1 documents of their own") ||
		!strings.Contains(out, "3 rows new, 0 already there") {
		t.Fatalf("first import: %v\n%s", err, out)
	}
	first, err := h.run("", "export")
	if err != nil || strings.Count(first, "\n") != 3 || strings.Count(first, `"archived":true`) != 3 {
		t.Fatalf("export: %v\n%s", err, first)
	}
	for _, want := range []string{`"project":"webshop"`, `"project":"billing"`, `"project":"facts"`, `"session":"aam:wing_billing"`} {
		if !strings.Contains(first, want) {
			t.Errorf("export lacks %s:\n%s", want, first)
		}
	}
	out, err = h.run("", "import-aam", file)
	if err != nil || !strings.Contains(out, "0 rows new, 3 already there") {
		t.Fatalf("second import: %v\n%s", err, out)
	}
	if second, _ := h.run("", "export"); second != first {
		t.Fatalf("a second import changed the rows:\n%s\nvs\n%s", first, second)
	}
}

func TestImportAAMFlags(t *testing.T) {
	h := newHome(t)
	file := memoryFile(t, 2)
	if out, err := h.run("", "import-aam", file); err == nil || !strings.Contains(out, "--team") {
		t.Fatalf("two teams and none named: %v\n%s", err, out)
	}
	if out, err := h.run("", "import-aam", file, "--team", "t1", "--dry-run"); err != nil || !strings.Contains(out, "would import 3 documents") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if got, _ := h.run("", "export"); got != "" {
		t.Fatalf("a dry run wrote: %s", got)
	}
	out, err := h.run("", "import-aam", file, "--team", "t1", "--wing", "billing", "--project-of", "billing=money")
	if err != nil || !strings.Contains(out, "imported 1 documents") || !strings.Contains(out, "1 facts belong to drawers of wings that were not asked for") {
		t.Fatalf("one wing: %v\n%s", err, out)
	}
	if got, _ := h.run("", "export"); strings.Count(got, "\n") != 1 || !strings.Contains(got, `"project":"money"`) || !strings.Contains(got, "credit notes") {
		t.Fatalf("the project of the wing, the chunk joined: %s", got)
	}
	if out, err := h.run("", "import-aam", file, "--team", "t1", "--project-of", "nonsense"); err == nil || !strings.Contains(out, "want wing=project") {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := h.run("", "import-aam"); err == nil || !strings.Contains(out, "usage") {
		t.Fatalf("%v\n%s", err, out)
	}
}
