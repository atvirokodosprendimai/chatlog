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
