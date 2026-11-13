// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/store"
	"github.com/atvirokodosprendimai/chatlog/internal/tei"
	"github.com/atvirokodosprendimai/chatlog/internal/vector"
)

func human(id, words string) string {
	return fmt.Sprintf(`{"type":"user","uuid":"%s","sessionId":"s","cwd":"/u/webshop","timestamp":"2026-10-28T10:00:00Z","origin":{"kind":"human"},"message":{"content":"%s"}}`+"\n", id, words)
}

func reply(id, text string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":"%s","sessionId":"s","timestamp":"2026-10-28T10:00:01Z","message":{"content":[{"type":"text","text":"%s"}]}}`+"\n", id, text)
}

func appendTo(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) (string, string, *Ingester) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p := filepath.Join(dir, "p", "a.jsonl")
	os.MkdirAll(filepath.Dir(p), 0o700)
	return dir, p, &Ingester{Store: st, Sources: []string{p}, Limit: 4000}
}

func count(t *testing.T, dir string) (n int, answers string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow(`SELECT count(*), coalesce(group_concat(answer,'|'),'') FROM (SELECT answer FROM exchanges ORDER BY id)`).Scan(&n, &answers); err != nil {
		t.Fatal(err)
	}
	return
}

func TestRestartAppendAndHalfLine(t *testing.T) {
	dir, p, in := setup(t)
	ctx := context.Background()
	appendTo(t, p, human("u1", "pirma")+reply("a1", "viena"))
	if err := in.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n, a := count(t, dir); n != 1 || a != "viena" {
		t.Fatalf("%d %q", n, a)
	}

	// Nothing new: a second scan, as after a restart, changes nothing and adds no copy.
	if err := in.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := count(t, dir); n != 1 {
		t.Fatalf("doubled: %d", n)
	}

	// The open exchange grows: the same row carries the new answer.
	appendTo(t, p, reply("a2", "antra"))
	if err := in.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n, a := count(t, dir); n != 1 || a != "antra" {
		t.Fatalf("%d %q", n, a)
	}

	// A half-written line waits; finishing it makes it count.
	line := human("u2", "antras")
	appendTo(t, p, line[:20])
	if err := in.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := count(t, dir); n != 1 {
		t.Fatalf("half line counted: %d", n)
	}
	appendTo(t, p, line[20:])
	if err := in.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := count(t, dir); n != 2 {
		t.Fatalf("%d", n)
	}
}

func TestRewrittenFileIsReadAgain(t *testing.T) {
	dir, p, in := setup(t)
	ctx := context.Background()
	appendTo(t, p, human("u1", "pirma")+reply("a1", "viena")+reply("a2", "dvi"))
	in.Scan(ctx)
	if err := os.WriteFile(p, []byte(human("u9", "kita")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := in.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := count(t, dir); n != 2 {
		t.Fatalf("%d", n)
	}
}

func TestSinceSkipsOlder(t *testing.T) {
	dir, p, in := setup(t)
	in.Since = time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	appendTo(t, p, human("u1", "sena"))
	if err := in.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, _ := count(t, dir); n != 0 {
		t.Fatalf("%d", n)
	}
}

func TestEmbedLaterWhenTEIReturns(t *testing.T) {
	dir, p, in := setup(t)
	ctx := context.Background()
	appendTo(t, p, human("u1", "pirma")+reply("a1", "viena"))
	if err := in.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	vec, err := vector.Open(filepath.Join(dir, "vec"))
	if err != nil {
		t.Fatal(err)
	}
	up := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up {
			http.Error(w, "down", http.StatusBadRequest)
			return
		}
		var req struct {
			Inputs []string `json:"inputs"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		out := make([][]float32, len(req.Inputs))
		for i := range out {
			out[i] = []float32{1, 0}
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	in.TEI, in.Vec = tei.New(srv.URL, srv.URL), vec

	if err := in.Embed(ctx, true); err == nil {
		t.Fatal("no error from a down TEI")
	}
	st, _ := in.Store.Stats(ctx)
	if st.Chunks != 1 || st.Pending != 1 {
		t.Fatalf("text not kept or pending wrong: %+v", st)
	}
	up = true
	if err := in.Embed(ctx, true); err != nil {
		t.Fatal(err)
	}
	st, _ = in.Store.Stats(ctx)
	if st.Pending != 0 || vec.Count() != 1 {
		t.Fatalf("%+v vec %d", st, vec.Count())
	}
}

// A search query waits in TEI's queue behind every bulk text in flight and gets 5 s, so few texts may be in flight at once.
func TestEmbedKeepsFewTextsInFlight(t *testing.T) {
	dir, p, in := setup(t)
	ctx := context.Background()
	for i := range 20 {
		appendTo(t, p, human(fmt.Sprintf("u%d", i), fmt.Sprintf("klausimas %d", i))+reply(fmt.Sprintf("a%d", i), fmt.Sprintf("atsakymas %d", i)))
	}
	if err := in.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	vec, err := vector.Open(filepath.Join(dir, "vec"))
	if err != nil {
		t.Fatal(err)
	}
	var inFlight, most atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Inputs []string `json:"inputs"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		n := inFlight.Add(int32(len(req.Inputs)))
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-int32(len(req.Inputs)))
		out := make([][]float32, len(req.Inputs))
		for i := range out {
			out[i] = []float32{1, 0}
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	in.TEI, in.Vec = tei.New(srv.URL, srv.URL), vec
	if err := in.Embed(ctx, true); err != nil {
		t.Fatal(err)
	}
	st, _ := in.Store.Stats(ctx)
	if st.Pending != 0 {
		t.Fatalf("pending %d after embed", st.Pending)
	}
	if m := most.Load(); m > 2 {
		t.Fatalf("%d texts in flight at once, want at most 2", m)
	}
}
