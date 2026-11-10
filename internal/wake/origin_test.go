// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package wake

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/chunk"
	"github.com/atvirokodosprendimai/chatlog/internal/ingest"
	"github.com/atvirokodosprendimai/chatlog/internal/search"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

// twoWindows ingests a main transcript and a subagent's, side by side as Claude Code lays them out.
func twoWindows(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	write := func(rel, words, answer string) {
		p := filepath.Join(dir, "projects", "-u-webshop", rel)
		os.MkdirAll(filepath.Dir(p), 0o700)
		lines := []string{
			fmt.Sprintf(`{"type":"user","uuid":"u-%s","sessionId":"s1","cwd":"/u/webshop","timestamp":"2026-11-08T09:00:00Z","origin":{"kind":"human"},"message":{"content":%q}}`, rel, words),
			fmt.Sprintf(`{"type":"assistant","uuid":"a-%s","sessionId":"s1","timestamp":"2026-11-08T09:00:05Z","message":{"content":[{"type":"text","text":%q}]}}`, rel, answer),
		}
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := (&ingest.Ingester{Store: st, Limit: chunk.DefaultLimit}).File(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	write("s1.jsonl", "what the user typed zebra", "the main answer zebra")
	write("s1/subagents/agent-a1.jsonl", "brief to the subagent zebra", "the subagent's finding zebra")
	return st
}

func TestWakeTailAndSearchShowOnlyTheUsersOwnSessionByDefault(t *testing.T) {
	ctx, st := context.Background(), twoWindows(t)
	d, err := Digest(ctx, st, Compact, "webshop", time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC), 24000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, "what the user typed zebra") || strings.Contains(d, "subagent") {
		t.Fatalf("a wake digest holds the user's own session only:\n%s", d)
	}
	items, _, err := st.Tail(ctx, "webshop", "", 0, 100000)
	if err != nil || len(items) != 1 || !strings.Contains(items[0].Text, "what the user typed") {
		t.Fatalf("chat_tail by default: %+v %v", items, err)
	}
	svc := &search.Service{Store: st}
	origins := func(r search.Request) int {
		t.Helper()
		r.Query, r.Mode = "zebra", search.ModeFTS
		resp, err := svc.Search(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		return len(resp.Hits)
	}
	if n := origins(search.Request{}); n != 1 {
		t.Errorf("chat_search by default: %d hits, want the user's own", n)
	}
	if n := origins(search.Request{Origin: store.OriginSubagent}); n != 1 {
		t.Errorf("origin subagent: %d", n)
	}
	if n := origins(search.Request{Origin: store.OriginAll}); n != 2 {
		t.Errorf("origin all: %d", n)
	}
	if _, err := svc.Search(ctx, search.Request{Query: "zebra", Origin: "everything"}); err == nil {
		t.Error("an unknown origin must be refused, not read as all")
	}
	if all, _, _ := st.Tail(ctx, "webshop", store.OriginAll, 0, 100000); len(all) != 2 {
		t.Errorf("tail with origin all: %d", len(all))
	}
}
