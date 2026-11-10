// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package aam

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

// schema is the tables this package reads, with the columns it reads and the others of the source's migrations left out
// where they hold vectors, billing or users: only what the memory is made of.
const schema = `
CREATE TABLE drawers (team_id TEXT NOT NULL, id TEXT NOT NULL, wing TEXT NOT NULL, room TEXT NOT NULL, source_file TEXT NOT NULL DEFAULT '',
  chunk_index INTEGER NOT NULL DEFAULT 0, content TEXT NOT NULL, entities TEXT NOT NULL DEFAULT '', parent_id TEXT NOT NULL DEFAULT '',
  filed_at TEXT NOT NULL, content_date TEXT NOT NULL DEFAULT '', agent TEXT NOT NULL DEFAULT '', topic TEXT NOT NULL DEFAULT '', embedded_at TEXT,
  valid_to TEXT NOT NULL DEFAULT '', superseded_by TEXT NOT NULL DEFAULT '', ended_reason TEXT NOT NULL DEFAULT '', ended_at TEXT NOT NULL DEFAULT '',
  content_key TEXT NOT NULL DEFAULT '', PRIMARY KEY (team_id, id));
CREATE TABLE kg_entities (team_id TEXT NOT NULL, id TEXT NOT NULL, name TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY (team_id, id));
CREATE TABLE kg_triples (team_id TEXT NOT NULL, id TEXT NOT NULL, subject TEXT NOT NULL, predicate TEXT NOT NULL, object TEXT NOT NULL,
  valid_from TEXT NOT NULL DEFAULT '', valid_to TEXT NOT NULL DEFAULT '', confidence REAL NOT NULL DEFAULT 1.0, source_closet TEXT NOT NULL DEFAULT '',
  source_file TEXT NOT NULL DEFAULT '', source_drawer_id TEXT NOT NULL DEFAULT '', extracted_at TEXT NOT NULL, derived INTEGER,
  ended_reason TEXT NOT NULL DEFAULT '', PRIMARY KEY (team_id, id));
CREATE TABLE vectors (id TEXT, embedding BLOB);
CREATE TABLE skills (id TEXT PRIMARY KEY, name TEXT, content TEXT);`

// Fixture writes a file shaped like the source's: two wings of one team, an oversized drawer in three chunks (the first
// two overlap, the last follows exactly), an ended one that was replaced, and facts with and without a drawer.
func Fixture(t testing.TB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "memory.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	over := strings.Repeat("overlapping words that two chunks share, ", 2)
	chunks := []string{"HEAD of a long decision: " + over, over + "the middle of it, then the cut falls mid-wo", "rd and the end."}
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	d := `INSERT INTO drawers(team_id, id, wing, room, source_file, chunk_index, content, parent_id, filed_at, content_date, topic, valid_to, superseded_by, ended_reason) VALUES('t1',?,?,?,?,?,?,?,?,?,?,?,?,?)`
	exec(d, "d-long", "wing_webshop", "decisions", "", 0, chunks[0], "", "2026-06-26T13:56:40.238357", "", "the long one", "", "", "")
	exec(d, "d-long-1", "wing_webshop", "decisions", "", 1, chunks[1], "d-long", "2026-06-26T13:56:40.238357", "", "", "", "", "")
	exec(d, "d-long-2", "wing_webshop", "decisions", "", 2, chunks[2], "d-long", "2026-06-26T13:56:40.238357", "", "", "", "", "")
	exec(d, "d-old", "wing_webshop", "decisions", "plan.md", 0, "use datastar for the forms", "", "2026-07-01T10:00:00Z", "", "", "2026-09-15T10:54:07Z", "d-new", "dropped on re-file")
	exec(d, "d-new", "wing_webshop", "decisions", "plan.md", 0, "use plain css :target for the forms", "", "2026-09-15T10:54:07Z", "2026-09-14", "", "", "", "")
	exec(d, "d-bill", "wing_billing", "facts", "", 0, "invoices are numbered per year", "", "2026-08-01T00:00:00+00:00", "", "", "", "", "")
	exec(d, "d-other-team", "wing_webshop", "decisions", "", 0, "another team's drawer", "", "2026-08-01T00:00:00Z", "", "", "", "", "")
	exec(`UPDATE drawers SET team_id = 't2' WHERE id = 'd-other-team'`)
	for id, name := range map[string]string{"web": "webshop frontend", "css": "plain css :target", "ds": "datastar", "inv": "invoices", "year": "per year", "x": "somewhere", "y": "nowhere"} {
		exec(`INSERT INTO kg_entities VALUES('t1', ?, ?, '2026-06-26')`, id, name)
	}
	tr := `INSERT INTO kg_triples(team_id, id, subject, predicate, object, valid_from, valid_to, source_file, source_drawer_id, extracted_at, ended_reason) VALUES('t1',?,?,?,?,?,?,?,?,?,?)`
	exec(tr, "f1", "web", "uses", "css", "2026-09-15", "", "", "d-new", "2026-09-15T11:00:00Z", "")
	exec(tr, "f2", "web", "used", "ds", "2026-07-01", "2026-09-15", "", "d-old", "2026-09-15T11:00:00Z", "replaced")
	exec(tr, "f3", "web", "has", "css", "", "", "", "d-long-2", "2026-09-15T11:00:00Z", "") // from a later chunk: the long drawer's
	exec(tr, "f4", "inv", "numbered", "year", "", "", "", "d-bill", "2026-09-15T11:00:00Z", "")
	exec(tr, "f5", "x", "relates", "y", "", "", "notes/readme.md", "", "2026-09-16T11:00:00Z", "") // no drawer, a source file
	exec(tr, "f6", "y", "relates", "x", "", "", "", "", "2026-09-17T11:00:00Z", "")                // no drawer, no source
	exec(tr, "f7", "x", "gone", "y", "", "", "", "d-vanished", "2026-09-17T11:00:00Z", "")         // its drawer is not in the file
	return path
}

func read(t *testing.T, path string, opt Options) (map[string]Doc, Stats) {
	t.Helper()
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	docs := map[string]Doc{}
	st, err := r.Read(context.Background(), opt, func(d Doc) error { docs[d.ID] = d; return nil })
	if err != nil {
		t.Fatal(err)
	}
	return docs, st
}

func TestADrawerIsOneDocumentWithItsChunksJoinedItsFactsAndItsEnd(t *testing.T) {
	docs, st := read(t, Fixture(t), Options{Team: "t1"})
	long := docs["d-long"]
	want := "HEAD of a long decision: " + strings.Repeat("overlapping words that two chunks share, ", 2) + "the middle of it, then the cut falls mid-word and the end."
	if !strings.HasPrefix(long.Text, want) || long.Wing != "wing_webshop" || long.Room != "decisions" || long.Topic != "the long one" {
		t.Fatalf("the chunks joined once, the repeat dropped, the cut word whole:\n%s", long.Text)
	}
	if !strings.HasSuffix(long.Text, "\n\nfacts:\nwebshop frontend has plain css :target") || long.Facts != 1 {
		t.Fatalf("a fact of a later chunk goes to the whole drawer:\n%s", long.Text)
	}
	if long.Filed != time.Date(2026, 6, 26, 13, 56, 40, 238357000, time.UTC) {
		t.Fatalf("a stamp without a zone is UTC: %v", long.Filed)
	}
	old := docs["d-old"]
	if !old.Ended || !strings.Contains(old.Text, "[ENDED 2026-09-15T10:54:07Z: dropped on re-file; superseded by d-new]") ||
		!strings.Contains(old.Text, "webshop frontend used datastar (2026-07-01..2026-09-15) [ended: replaced]") {
		t.Fatalf("an ended drawer says so, and its fact holds its own end:\n%s", old.Text)
	}
	if docs["d-new"].Ended || !strings.Contains(docs["d-new"].Text, "webshop frontend uses plain css :target (from 2026-09-15)") {
		t.Fatalf("%+v", docs["d-new"])
	}
	// links: both ends of a replacement share one; a fact's entities and a source file link drawers
	if !slices.Contains(old.Refs, "sup:d-old>d-new") || !slices.Contains(docs["d-new"].Refs, "sup:d-old>d-new") {
		t.Fatalf("replacement: %v %v", old.Refs, docs["d-new"].Refs)
	}
	if !slices.Contains(old.Refs, "src:plan.md") || !slices.Contains(docs["d-new"].Refs, "src:plan.md") || !slices.Contains(old.Refs, "ent:web") || !slices.Contains(docs["d-new"].Refs, "ent:css") {
		t.Fatalf("source and entities: %v %v", old.Refs, docs["d-new"].Refs)
	}
	if _, ok := docs["d-other-team"]; ok {
		t.Fatal("another team's drawer")
	}
	if st.Drawers != 4 || st.Joined != 1 || st.Ended != 1 || st.Facts != 4 {
		t.Fatalf("%+v", st)
	}
}

func TestFactsWithoutADrawerGetDocumentsOfTheirOwnGroupedBySource(t *testing.T) {
	docs, st := read(t, Fixture(t), Options{Team: "t1"})
	var loose []Doc
	for id, d := range docs {
		if strings.HasPrefix(id, "facts:") {
			loose = append(loose, d)
		}
	}
	slices.SortFunc(loose, func(a, b Doc) int { return strings.Compare(a.Room, b.Room) })
	if len(loose) != 2 || st.FactDocs != 2 || st.LooseFacts != 3 {
		t.Fatalf("%d documents, %+v", len(loose), st)
	}
	if loose[0].Room != "no source" || loose[0].Wing != FactsWing || !strings.Contains(loose[0].Text, "nowhere relates somewhere") || !strings.Contains(loose[0].Text, "somewhere gone nowhere") {
		t.Fatalf("%+v", loose[0])
	}
	if loose[1].Room != "notes/readme.md" || !strings.Contains(loose[1].Text, "somewhere relates nowhere") || !slices.Contains(loose[1].Refs, "src:notes/readme.md") {
		t.Fatalf("%+v", loose[1])
	}
	again, _ := read(t, Fixture(t), Options{Team: "t1"})
	for id := range docs {
		if _, ok := again[id]; !ok {
			t.Fatalf("an id changed between two reads of the same content: %s", id)
		}
	}
	// facts of drawers of a wing that was not asked for are left out, and no wing owns the loose ones
	docs, st = read(t, Fixture(t), Options{Team: "t1", Wings: []string{"billing"}})
	if len(docs) != 1 || docs["d-bill"].Wing != "wing_billing" || st.SkippedFacts != 3 || st.Facts != 1 || st.LooseFacts != 0 {
		t.Fatalf("%d docs %+v", len(docs), st)
	}
}

func TestPickTeamAndOpenRefuseWhatIsNotAFileOfThisKind(t *testing.T) {
	path := Fixture(t)
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	teams, _ := r.Teams(context.Background())
	if len(teams) != 2 {
		t.Fatal(teams)
	}
	if _, err := PickTeam(teams, ""); err == nil || !strings.Contains(err.Error(), "--team") {
		t.Errorf("two teams and none named: %v", err)
	}
	if got, err := PickTeam(teams, "t1"); err != nil || got != "t1" {
		t.Errorf("%s %v", got, err)
	}
	if _, err := PickTeam(teams, "nope"); err == nil {
		t.Error("an unknown team")
	}
	if got, _ := PickTeam(map[string]int{"only": 3}, ""); got != "only" {
		t.Errorf("the only team: %s", got)
	}
	other := filepath.Join(t.TempDir(), "x.sqlite")
	db, _ := sql.Open("sqlite", other)
	db.Exec(`CREATE TABLE t(a)`)
	db.Close()
	if _, err := Open(other); err == nil || !strings.Contains(err.Error(), "not a file of this kind") {
		t.Errorf("%v", err)
	}
	if _, err := Open(filepath.Join(t.TempDir(), "absent.sqlite")); err == nil {
		t.Error("a file that is not there")
	}
}

// The source is read-only: nothing is created beside it, not a journal and not a wal.
func TestTheSourceIsNeverWritten(t *testing.T) {
	path := Fixture(t)
	before, _ := os.ReadFile(path)
	read(t, path, Options{Team: "t1"})
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("the file changed")
	}
	left, _ := filepath.Glob(path + "*")
	if len(left) != 1 {
		t.Fatalf("files beside the source: %v", left)
	}
}

func TestJoinLineAndParseTime(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{nil, ""}, {[]string{"a"}, "a"}, {[]string{"abc", "def"}, "abcdef"},
		{[]string{"ABCDEFGHIJKLMNOPQRSTUVWXYZ", "GHIJKLMNOPQRSTUVWXYZ0123"}, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123"},
		{[]string{"short end 12345", "12345 short start"}, "short end 1234512345 short start"}, // a repeat under 20 bytes is not one
	} {
		if got := Join(c.in); got != c.want {
			t.Errorf("Join(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := Line("a", "p", "b", "", "", ""); got != "a p b" {
		t.Error(got)
	}
	for _, c := range []struct{ from, to, reason, want string }{
		{"2026-01-01", "", "", "a p b (from 2026-01-01)"}, {"", "2026-02-01", "", "a p b (until 2026-02-01)"},
		{"2026-01-01", "2026-02-01", "moved", "a p b (2026-01-01..2026-02-01) [ended: moved]"},
	} {
		if got := Line("a", "p", "b", c.from, c.to, c.reason); got != c.want {
			t.Errorf("%q", got)
		}
	}
	for in, want := range map[string]string{
		"2026-06-26T13:56:40.238357": "2026-06-26T13:56:40.238357Z", "2026-06-26T13:56:40Z": "2026-06-26T13:56:40Z",
		"2026-08-01T02:00:00+02:00": "2026-08-01T00:00:00Z", "2026-06-26": "2026-06-26T00:00:00Z", "": "0001-01-01T00:00:00Z", "junk": "0001-01-01T00:00:00Z",
	} {
		if got := ParseTime(in).Format("2006-01-02T15:04:05.999999999Z07:00"); got != want {
			t.Errorf("ParseTime(%q) = %s, want %s", in, got, want)
		}
	}
	if got := ParseTime("junk", "2026-06-26"); got.Day() != 26 {
		t.Errorf("the date the memory is about is the second choice: %v", got)
	}
	_ = fmt.Sprint
}
