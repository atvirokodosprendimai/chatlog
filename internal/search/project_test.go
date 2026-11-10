// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package search

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

func TestRepoTakesAnAliasAShortShaOrAPathWord(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	keyA, keyB := "526abcd0000000000000000000000000000000aa", "9f3e21a0000000000000000000000000000000bb"
	at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	for _, r := range []store.Row{
		{UUID: "a", Session: "s", Source: "f", Time: at, End: at, Cwd: "/u/work/api-gateway/scripts", Project: "api-gateway", PKey: keyA, Words: "w", Answer: "a", Sha: "a", Text: "needle in a"},
		{UUID: "b", Session: "s", Source: "f", Time: at, End: at, Cwd: "/u/work/billing", Project: "billing", PKey: keyB, Words: "w", Answer: "a", Sha: "b", Text: "needle in b"},
		{UUID: "old", Session: "s", Source: "f", Time: at, End: at, Cwd: "/u/work/api-gateway", Project: "api-gateway", Words: "w", Answer: "a", Sha: "o", Text: "needle in an exchange from before keys"},
	} {
		if err := st.Apply(ctx, "f-"+r.UUID, []store.Row{r}, store.Cursor{}); err != nil {
			t.Fatal(err)
		}
	}
	st.AddAlias(ctx, keyA, "gw")
	svc := &Service{Store: st}
	for repo, want := range map[string]int{
		"": 3, "gw": 2, "api-gateway": 2, "[526abcd]": 2, "526abcd": 2, keyA: 2, "billing": 1, "9f3e": 1, "scripts": 1, "work": 3,
	} {
		resp, err := svc.Search(ctx, Request{Query: "needle", Mode: ModeFTS, Repo: repo})
		if err != nil || len(resp.Hits) != want {
			t.Errorf("repo %q: %d hits, want %d (%v)", repo, len(resp.Hits), want, err)
		}
	}
}

func TestTheArchiveIsSearchedOnlyWhenAskedAndMarked(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	for _, r := range []store.Row{
		{UUID: "live", Session: "s", Source: "f", Time: at, End: at, Project: "p", Words: "w", Answer: "a", Sha: "l", Text: "needle live"},
		{UUID: "old", Session: "s", Source: "f", Time: at, End: at, Project: "p", Words: "w", Answer: "a", Sha: "o", Text: "needle archived", Archived: true},
	} {
		if err := st.Apply(ctx, "f-"+r.UUID, []store.Row{r}, store.Cursor{}); err != nil {
			t.Fatal(err)
		}
	}
	svc := &Service{Store: st}
	hits := func(archive bool) (n int, archived int) {
		resp, err := svc.Search(ctx, Request{Query: "needle", Mode: ModeFTS, Archive: archive})
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range resp.Hits {
			if h.Archived {
				archived++
			}
		}
		return len(resp.Hits), archived
	}
	if n, a := hits(false); n != 1 || a != 0 {
		t.Errorf("by default: %d hits, %d archived", n, a)
	}
	if n, a := hits(true); n != 2 || a != 1 {
		t.Errorf("with the archive: %d hits, %d marked archived", n, a)
	}
	var id int64
	all, _ := svc.Search(ctx, Request{Query: "needle", Mode: ModeFTS, Archive: true})
	for _, h := range all.Hits {
		if h.Archived {
			id = h.ID
		}
	}
	if _, err := st.SetArchived(ctx, id, false, "still holds"); err != nil {
		t.Fatal(err)
	}
	if n, a := hits(false); n != 2 || a != 0 {
		t.Errorf("after the restore, found by default: %d hits, %d archived", n, a)
	}
}
