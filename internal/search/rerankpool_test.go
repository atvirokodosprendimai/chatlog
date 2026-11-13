// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/store"
	"github.com/atvirokodosprendimai/chatlog/internal/tei"
	"github.com/atvirokodosprendimai/chatlog/internal/vector"
)

// A reranker that shares its GPU with the embedder: 50 texts did not fit rerankWait, so only the head of the pool is reranked.
func TestRerankSendsOnlyTheHeadOfThePool(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for i := range 30 {
		u := fmt.Sprintf("u%02d", i)
		at := midnight.Add(time.Duration(i) * time.Minute)
		r := store.Row{UUID: u, Session: "s", Source: "f", Time: at, End: at, Project: "p", Words: "zebra question", Answer: "zebra answer", Sha: u, Text: "zebra question\nzebra answer"}
		if err := st.Apply(context.Background(), "f-"+u, []store.Row{r}, store.Cursor{}); err != nil {
			t.Fatal(err)
		}
	}
	sent := 0
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/embed" {
			json.NewEncoder(w).Encode([][]float32{{1, 0}})
			return
		}
		var req struct {
			Texts []string `json:"texts"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		sent = len(req.Texts)
		out := []map[string]any{}
		for i := range req.Texts {
			out = append(out, map[string]any{"index": i, "score": float64(i)}) // reverses the head
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer fake.Close()
	vec, err := vector.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: st, Vec: vec, TEI: tei.New(fake.URL, fake.URL)}
	got, err := svc.Search(context.Background(), Request{Query: "zebra", Mode: ModeRerank, Limit: 20})
	if err != nil || len(got.Degraded) != 0 {
		t.Fatalf("%v %v", err, got.Degraded)
	}
	if sent == 0 || sent > rerankPool {
		t.Fatalf("reranked %d texts, want 1..%d", sent, rerankPool)
	}
	if len(got.Hits) != 20 {
		t.Fatalf("%d hits, want 20: the unreranked tail still fills the limit", len(got.Hits))
	}
}
