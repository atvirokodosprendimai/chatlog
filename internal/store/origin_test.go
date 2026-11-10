// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"slices"
	"testing"
	"time"
)

// An exchange typed into a subagent's window carries an agent id; the user's own has none. Every read defaults to the latter.
func TestOriginFilterReachesSearchTailAndRecent(t *testing.T) {
	ctx, s := context.Background(), open(t)
	for i, agent := range []string{"", "a1"} {
		at := time.Date(2026, 11, 2, 10, i, 0, 0, time.UTC)
		r := Row{UUID: "u" + agent, Session: "s", Agent: agent, Source: "f", Time: at, End: at, Project: "webshop", Words: "w", Answer: "a", Sha: agent, Text: "needle " + agent}
		if err := s.Apply(ctx, "f-"+agent, []Row{r}, Cursor{}); err != nil {
			t.Fatal(err)
		}
	}
	found := func(o string) []string {
		t.Helper()
		f := Filter{Origin: o}
		ids, err := s.FTS(ctx, "needle", 10, f)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := s.Rows(ctx, ids, f, false, false)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range rows {
			got = append(got, r.Agent)
		}
		slices.Sort(got)
		return got
	}
	for o, want := range map[string][]string{"": {""}, OriginMain: {""}, OriginSubagent: {"a1"}, OriginAll: {"", "a1"}} {
		if got := found(o); !slices.Equal(got, want) {
			t.Errorf("origin %q finds agents %q, want %q", o, got, want)
		}
	}
	if items, _, _ := s.Tail(ctx, "webshop", "", 0, 1000); texts(items) != "needle " {
		t.Errorf("tail by default is the user's own: %q", texts(items))
	}
	if items, _, _ := s.Tail(ctx, "webshop", OriginSubagent, 0, 1000); texts(items) != "needle a1" {
		t.Errorf("tail of subagents: %q", texts(items))
	}
	if items, _, _ := s.Tail(ctx, "webshop", OriginAll, 0, 1000); len(items) != 2 {
		t.Errorf("tail of all: %q", texts(items))
	}
	if items, _ := s.Recent(ctx, "webshop", OriginMain, 10, 100); len(items) != 1 || items[0].Agent != "" {
		t.Errorf("recent, which the wake digest reads: %+v", items)
	}
	for _, o := range []string{"", "main", "subagent", "all"} {
		if !ValidOrigin(o) {
			t.Errorf("%q is an origin", o)
		}
	}
	if ValidOrigin("fleet") || ValidOrigin("everything") {
		t.Error("an unknown origin is refused, not read as all")
	}
}
