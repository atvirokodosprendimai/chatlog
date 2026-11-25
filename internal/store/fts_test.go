// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// readCalls is how many read syscalls this process made so far: each SQLite page that misses its own cache is one pread.
func readCalls(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile("/proc/self/io")
	if err != nil {
		t.Skip("no /proc/self/io on this system:", err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "syscr: "); ok {
			n, _ := strconv.Atoi(v)
			return n
		}
	}
	t.Fatal("no syscr in /proc/self/io")
	return 0
}

// A query word found in thousands of exchanges must not read every one of those rows to rank them: on a disk that
// caches no data, each row read is a disk read, and a search over the whole history took seconds.
func TestFTSReadsTheRowsOfTheTopMatchesNotOfEveryMatch(t *testing.T) {
	ctx, s := context.Background(), open(t)
	at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	const matches = 4000
	pad := strings.Repeat("filler ", 500) // ~3.5 KB: each row has a page of its own, as the real rows do
	rows := make([]Row, matches)
	for i := range rows {
		rows[i] = Row{UUID: fmt.Sprint("r", i), Session: "s", Source: "f", Time: at, End: at, Project: "p", Words: "w", Answer: "needle", Sha: fmt.Sprint(i), Text: pad}
	}
	if err := s.Apply(ctx, "f", rows, Cursor{}); err != nil {
		t.Fatal(err)
	}
	before := readCalls(t)
	ids, err := s.FTS(ctx, "needle", 10, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	reads := readCalls(t) - before
	if len(ids) != 10 {
		t.Fatalf("got %d ids, want 10", len(ids))
	}
	if reads > matches/4 {
		t.Fatalf("FTS made %d reads for 10 hits out of %d matches: it reads a row per match", reads, matches)
	}
}

// The ranking is the one the plain join gives: the best bm25 among the rows that pass the filter, even when most of the
// best matches are filtered out.
func TestFTSRanksAsTheJoinDoesWhenTheFilterDropsMostMatches(t *testing.T) {
	ctx, s := context.Background(), open(t)
	at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	var rows []Row
	for i := range 600 {
		// the more needles, the better the match; nine of ten rows are archived, the best ones first
		text := strings.Repeat("needle ", 1+i%37) + strings.Repeat("hay ", 1+i%11)
		rows = append(rows, Row{UUID: fmt.Sprint("r", i), Session: "s", Source: "f", Time: at.Add(time.Duration(i) * time.Minute), End: at, Project: "p",
			Words: "w", Answer: "a", Sha: fmt.Sprint(i), Text: text, Archived: i%10 != 0})
	}
	if err := s.Apply(ctx, "f", rows, Cursor{}); err != nil {
		t.Fatal(err)
	}
	join := func(n int, f Filter) []int64 {
		where, args := f.where()
		q := `SELECT e.id FROM fts JOIN exchanges e ON e.id = fts.rowid WHERE fts MATCH ?` + where + ` ORDER BY bm25(fts, 5.0, 3.0, 1.0), e.id LIMIT ?`
		r, err := s.r.QueryContext(ctx, q, append(append([]any{ftsMatch(Terms("needle hay"), f.Role)}, args...), n)...)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		var out []int64
		for r.Next() {
			var id int64
			if err := r.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		return out
	}
	for _, c := range []struct {
		n int
		f Filter
	}{{5, Filter{}}, {50, Filter{}}, {100, Filter{}}, {7, Filter{Archive: true}}, {20, Filter{Since: at.Add(500 * time.Minute)}}, {10, Filter{Origin: OriginSubagent}}} {
		got, err := s.FTS(ctx, "needle hay", c.n, c.f)
		if err != nil {
			t.Fatal(err)
		}
		if want := join(c.n, c.f); !slices.Equal(got, want) {
			t.Errorf("n=%d filter=%+v:\n got %v\nwant %v", c.n, c.f, got, want)
		}
	}
}
