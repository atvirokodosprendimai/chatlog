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
	"github.com/atvirokodosprendimai/chatlog/internal/playbook"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

// session writes a transcript of three exchanges, each with tool calls, and ingests it.
func session(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var lines []string
	for i, tool := range []string{"Bash", "Grep", "Edit"} {
		m := 10 * i
		lines = append(lines,
			fmt.Sprintf(`{"type":"user","uuid":"u%d","sessionId":"s","cwd":"/u/webshop","timestamp":"2026-11-08T09:%02d:00Z","origin":{"kind":"human"},"message":{"content":"step %d please"}}`, i, m, i),
			fmt.Sprintf(`{"type":"assistant","uuid":"a%d","sessionId":"s","timestamp":"2026-11-08T09:%02d:01Z","message":{"content":[{"type":"tool_use","id":"t%d","name":"%s","input":{"command":"do-%d"}}]}}`, i, m, i, tool, i),
			fmt.Sprintf(`{"type":"user","uuid":"r%d","sessionId":"s","timestamp":"2026-11-08T09:%02d:03Z","message":{"content":[{"type":"tool_result","tool_use_id":"t%d","content":"done %d"}]}}`, i, m, i, i),
			fmt.Sprintf(`{"type":"assistant","uuid":"b%d","sessionId":"s","timestamp":"2026-11-08T09:%02d:04Z","message":{"content":[{"type":"text","text":"finished step %d"}]}}`, i, m, i))
	}
	p := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&ingest.Ingester{Store: st, Limit: chunk.DefaultLimit}).File(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := st.KVWrite(context.Background(), "webshop", map[string]string{"now/release": "STATE building v2", "task/open/1-docs": "1/3 done: next INSTALL.md", "fact/x": "not in a clear"}, nil); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestCompactDigestCarriesTheToolCallsUnderTheCap(t *testing.T) {
	st := session(t)
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	d, err := Digest(context.Background(), st, Compact, "webshop", now, 24000)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{playbook.FirstLine, "THE CONVERSATION WAS COMPACTED", "now/release = STATE building v2", "task/open/1-docs",
		"step 0 please", "tool Bash 09:00:01Z 2s: do-0", "tool Grep 09:10:01Z 2s: do-1", "tool Edit 09:20:01Z 2s: do-2", "answer: finished step 2",
		`chat_tail project="webshop" before=`} {
		if !strings.Contains(d, want) {
			t.Fatalf("missing %q in:\n%s", want, d)
		}
	}
	if len(d) > 24000 {
		t.Fatalf("over the cap: %d", len(d))
	}
	if strings.Index(d, "step 0") > strings.Index(d, "step 2") {
		t.Fatal("exchanges must come oldest first")
	}
}

func TestClearIsLightAndTheCapCuts(t *testing.T) {
	st := session(t)
	now := time.Now()
	d, _ := Digest(context.Background(), st, Clear, "webshop", now, 24000)
	if strings.Contains(d, "step 0") || strings.Contains(d, "fact/x") || !strings.Contains(d, "now/release") {
		t.Fatalf("a clear carries notes of its prefixes and no exchanges:\n%s", d)
	}
	small, _ := Digest(context.Background(), st, Compact, "webshop", now, 1500)
	if len(small) > 1500 || !strings.HasPrefix(small, playbook.FirstLine) || !strings.Contains(small, "cut at the digest's cap") || !strings.Contains(small, "## More") {
		t.Fatalf("a small cap must cut, say so, and keep the rule and the pointers (%d bytes):\n%s", len(small), small)
	}
	tiny, _ := Digest(context.Background(), st, Compact, "webshop", now, 100)
	if !strings.Contains(tiny, strings.TrimRight(playbook.Rule, "\n")) {
		t.Fatalf("the rule must come whole whatever the cap:\n%s", tiny)
	}
	if _, err := Digest(context.Background(), st, "nap", "webshop", now, 1000); err == nil {
		t.Fatal("an unknown kind must be refused")
	}
}
