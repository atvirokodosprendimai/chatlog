// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// putArchived stores an archived row, as an import does, with the links that join it to others.
func putArchived(t *testing.T, s *Store, uuid, text string, refs ...string) int64 {
	t.Helper()
	at := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	r := Row{UUID: uuid, Session: "import", Source: "f", Time: at, End: at, Project: "webshop", Words: "room " + uuid, Answer: text, Sha: uuid + text, Text: text, Archived: true, Refs: refs}
	if err := s.Apply(context.Background(), "f-"+uuid, []Row{r}, Cursor{}); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := s.r.QueryRow(`SELECT id FROM exchanges WHERE uuid = ?`, uuid).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func found(t *testing.T, s *Store, f Filter) []string {
	t.Helper()
	ids, err := s.FTS(context.Background(), "needle", 50, f)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.Rows(context.Background(), ids, f, false, false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, r.UUID)
	}
	slices.Sort(got)
	return got
}

func TestArchivedRowsAreHiddenUnlessTheArchiveIsAsked(t *testing.T) {
	ctx, s := context.Background(), open(t)
	put(t, s, "webshop", 1, "needle live")
	old := putArchived(t, s, "old", "needle archived")
	if got := found(t, s, Filter{}); !slices.Equal(got, []string{"webshop-1"}) {
		t.Fatalf("by default: %v", got)
	}
	if got := found(t, s, Filter{Archive: true}); !slices.Equal(got, []string{"old", "webshop-1"}) {
		t.Fatalf("with the archive: %v", got)
	}
	if got := found(t, s, Filter{Archive: true, Origin: OriginSubagent}); len(got) != 0 {
		t.Fatalf("the archive widens nothing else: %v", got)
	}
	// the tail and the wake digest's recent exchanges never hold them, not even for every origin
	for _, o := range []string{"", OriginAll} {
		if items, _, _ := s.Tail(ctx, "webshop", o, 0, 100000); texts(items) != "needle live" {
			t.Errorf("tail %q: %q", o, texts(items))
		}
		if items, _ := s.Recent(ctx, "webshop", o, 10, 100); len(items) != 1 {
			t.Errorf("recent %q: %d", o, len(items))
		}
	}
	// a row asked for by id is still given, marked
	if r, err := s.Get(ctx, old); err != nil || !r.Archived {
		t.Errorf("get: %+v %v", r, err)
	}
	if st, _ := s.Stats(ctx); st.Archived != 1 || st.Chunks != 2 {
		t.Errorf("status: %+v", st)
	}
}

func TestRestoringOneRowNeedsAWhyAndRestoresOnlyThatRow(t *testing.T) {
	ctx, s := context.Background(), open(t)
	a := putArchived(t, s, "a", "needle a", "drawer:1", "ent:db", "ent:cache")
	b := putArchived(t, s, "b", "needle b", "ent:db", "ent:cache") // shares two links with a
	c := putArchived(t, s, "c", "needle c", "ent:db")              // shares one
	putArchived(t, s, "d", "needle d", "ent:other")                // none
	if _, err := s.SetArchived(ctx, a, false, " "); !errors.Is(err, ErrRefused) {
		t.Fatalf("no why: %v", err)
	}
	if _, err := s.SetArchived(ctx, 99999, false, "x"); !errors.Is(err, ErrRefused) {
		t.Fatalf("no such row: %v", err)
	}
	res, err := s.SetArchived(ctx, a, false, "the schema still has the table, checked in migrations/003.sql")
	if err != nil || !res.Changed || res.ID != a {
		t.Fatalf("%+v %v", res, err)
	}
	if got := found(t, s, Filter{}); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("only that row is restored: %v", got)
	}
	if len(res.Neighbours) != 2 || res.Neighbours[0].ID != b || res.Neighbours[1].ID != c || res.More != 0 {
		t.Fatalf("neighbours, the best linked first, the unlinked left out: %+v", res)
	}
	if n := res.Neighbours[0]; !strings.HasPrefix(n.Line, "room b: needle b") || !slices.Equal(n.Via, []string{"ent:db", "ent:cache"}) && !slices.Equal(n.Via, []string{"ent:cache", "ent:db"}) {
		t.Fatalf("an id, a line and what links it: %+v", n)
	}
	// the caller restores a neighbour that holds, and its own neighbours are then asked for; a restored row is not listed again
	res, err = s.SetArchived(ctx, b, false, "same")
	if err != nil || len(res.Neighbours) != 1 || res.Neighbours[0].ID != c {
		t.Fatalf("%+v %v", res, err)
	}
	// restoring twice changes nothing and still lists the neighbours; archiving puts it back
	if res, _ := s.SetArchived(ctx, b, false, "again"); res.Changed {
		t.Error("a row that is not archived is not changed")
	}
	if res, err := s.SetArchived(ctx, b, true, ""); err != nil || !res.Changed || len(res.Neighbours) != 0 {
		t.Fatalf("archive again: %+v %v", res, err)
	}
	if got := found(t, s, Filter{}); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("%v", got)
	}
	// the history is the events table: who, why and when, for each change
	evs, _ := s.Events(ctx, "", 20)
	var lines []string
	for _, e := range evs {
		if e.Event == EventArchive {
			lines = append(lines, e.Input)
		}
	}
	want := []string{
		fmt.Sprintf("id=%d restored: the schema still has the table, checked in migrations/003.sql", a),
		fmt.Sprintf("id=%d restored: same", b),
		fmt.Sprintf("id=%d archived: ", b),
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("events %q, want %q", lines, want)
	}
}

func TestNeighboursAreCappedAndCounted(t *testing.T) {
	ctx, s := context.Background(), open(t)
	first := putArchived(t, s, "first", "needle", "ent:hub")
	for i := 0; i < NeighbourMax+5; i++ {
		putArchived(t, s, fmt.Sprintf("n%02d", i), "needle", "ent:hub")
	}
	res, err := s.SetArchived(ctx, first, false, "checked")
	if err != nil || len(res.Neighbours) != NeighbourMax || res.More != 5 {
		t.Fatalf("%d neighbours, %d more, %v", len(res.Neighbours), res.More, err)
	}
}

// An import run again must change nothing: not the links, and not a row someone restored in between.
func TestImportingAgainDoesNotArchiveARestoredRowAgain(t *testing.T) {
	ctx, s := context.Background(), open(t)
	id := putArchived(t, s, "x", "needle x", "ent:a")
	if _, err := s.SetArchived(ctx, id, false, "checked"); err != nil {
		t.Fatal(err)
	}
	putArchived(t, s, "x", "needle x", "ent:a", "ent:b")
	if got := found(t, s, Filter{}); !slices.Equal(got, []string{"x"}) {
		t.Fatalf("the second import archived it again: %v", got)
	}
	var n int
	s.r.QueryRow(`SELECT count(*) FROM row_ref WHERE row = ?`, id).Scan(&n)
	if n != 2 {
		t.Fatalf("links: %d, want the two distinct refs once each", n)
	}
	if st, _ := s.Stats(ctx); st.Chunks != 1 {
		t.Fatalf("a second import doubled a row: %+v", st)
	}
}
