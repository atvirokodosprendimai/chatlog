// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/search"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

func server(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "chatlog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := httptest.NewServer(Handler(&search.Service{Store: st}, nil))
	t.Cleanup(srv.Close)
	return srv, st
}

func post(t *testing.T, srv *httptest.Server, path, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	b.ReadFrom(resp.Body)
	return resp.StatusCode, b.String()
}

func TestKVReadFollowsEdgesBothWaysAndByObject(t *testing.T) {
	srv, st := server(t)
	ctx := context.Background()
	st.KVWrite(ctx, "p", map[string]string{"waits/m/image": "WAIT M", "now/jur/api": "STATE live", "fact/x": "x"}, nil)
	if code, body := post(t, srv, "/kv/link", `{"project":"p","add":[{"s":"waits/m/image","p":"blocked_by","o":"now/jur/api"},{"s":"now/jur/api","p":"applies_to","o":"island/juridiniai"}]}`); code != 200 || !strings.Contains(body, `"added":2`) {
		t.Fatalf("link: %d %s", code, body)
	}
	if code, body := post(t, srv, "/kv/link", `{"project":"p","add":[{"s":"fact/x","p":"causes","o":"y"}]}`); code != 400 {
		t.Fatalf("a predicate not on the list: %d %s", code, body)
	}
	_, body := post(t, srv, "/kv/read", `{"project":"p","keys":["now/jur/api"],"follow":1}`)
	var got []store.KV
	if err := json.Unmarshal([]byte(body), &got); err != nil || len(got) != 1 {
		t.Fatalf("%s %v", body, err)
	}
	if len(got[0].Out) != 1 || got[0].Out[0] != (store.Edge{P: "applies_to", O: "island/juridiniai"}) ||
		len(got[0].In) != 1 || got[0].In[0] != (store.Edge{S: "waits/m/image", P: "blocked_by"}) {
		t.Fatalf("one hop out and in: %s", body)
	}
	_, body = post(t, srv, "/kv/read", `{"project":"p","linked_to":"island/juridiniai"}`)
	if json.Unmarshal([]byte(body), &got); len(got) != 1 || got[0].Key != "now/jur/api" {
		t.Fatalf("linked_to: %s", body)
	}
	if _, body = post(t, srv, "/kv/read", `{"project":"p","linked_to":"island/none"}`); strings.TrimSpace(body) != "[]" {
		t.Fatalf("nothing linked is an empty list: %s", body)
	}
	if code, _ := post(t, srv, "/kv/read", `{"project":"p","keys":["fact/x"],"follow":2}`); code != 400 {
		t.Fatalf("follow 2: %d", code)
	}
}

func TestKVReadWithoutFollowCarriesNoEdges(t *testing.T) {
	srv, st := server(t)
	ctx := context.Background()
	st.KVWrite(ctx, "p", map[string]string{"fact/a": "v", "fact/b": "w"}, nil)
	st.KVLink(ctx, "p", []store.Link{{S: "fact/a", P: "see_also", O: "fact/b"}}, nil)
	_, body := post(t, srv, "/kv/read", `{"project":"p","prefixes":["fact/"]}`)
	var raw []map[string]any
	if err := json.Unmarshal([]byte(body), &raw); err != nil || len(raw) != 2 {
		t.Fatalf("%s %v", body, err)
	}
	for _, e := range raw {
		for k := range e {
			if k != "key" && k != "value" && k != "project" && k != "time" && k != "age" {
				t.Fatalf("default read carries %q: %s", k, body)
			}
		}
	}
}

// A script or hook from before projects sends no project: it is read as the CLI reads a call, by its cwd when it sends
// one and else as the global notes, never refused.
func TestARequestWithoutAProjectResolvesLikeTheCLI(t *testing.T) {
	srv, st := server(t)
	cwd := t.TempDir()
	if code, msg := post(t, srv, "/kv/write", `{"set":{"a":"global note"}}`); code != 200 {
		t.Fatalf("write without a project: %d %q", code, msg)
	}
	if code, msg := post(t, srv, "/kv/write", `{"cwd":`+strconv.Quote(cwd)+`,"set":{"a":"cwd note"}}`); code != 200 {
		t.Fatalf("write with a cwd: %d %q", code, msg)
	}
	if code, msg := post(t, srv, "/kv/link", `{"add":[{"s":"a","p":"see_also","o":"x"}]}`); code != 200 {
		t.Fatalf("link without a project: %d %q", code, msg)
	}
	if code, msg := post(t, srv, "/kv/read", `{"prefixes":[""]}`); code != 200 || !strings.Contains(msg, "global note") || strings.Contains(msg, "cwd note") {
		t.Errorf("read without a project is the global notes: %d %q", code, msg)
	}
	if code, msg := post(t, srv, "/kv/read", `{"cwd":`+strconv.Quote(cwd)+`,"prefixes":[""]}`); code != 200 || !strings.Contains(msg, "cwd note") || strings.Contains(msg, "global note") {
		t.Errorf("read with a cwd is that directory's project: %d %q", code, msg)
	}
	// the search has no embedder in this server: its project is resolved by the same function
	if got := requestProject(context.Background(), st, "", ""); got != store.GlobalProject {
		t.Errorf("search without a project: %q", got)
	}
	if code, _ := post(t, srv, "/kv/write", `{"project":"*","set":{"a":"b"}}`); code != 400 {
		t.Errorf("a write for every project: %d", code)
	}
}

func TestOneProjectsNotesAreNotAnothersAcrossTheSocket(t *testing.T) {
	srv, _ := server(t)
	post(t, srv, "/kv/write", `{"project":"alpha","set":{"now/x":"alpha's","global/rule/r":"everyone's"}}`)
	read := func(project string) string {
		_, body := post(t, srv, "/kv/read", `{"project":"`+project+`","prefixes":[""]}`)
		return body
	}
	if b := read("alpha"); !strings.Contains(b, "alpha's") || !strings.Contains(b, "everyone's") {
		t.Errorf("alpha: %s", b)
	}
	if b := read("beta"); strings.Contains(b, "alpha's") || !strings.Contains(b, "everyone's") {
		t.Errorf("beta: %s", b)
	}
	if b := read("*"); !strings.Contains(b, "alpha's") {
		t.Errorf("every project: %s", b)
	}
}

func TestArchiveFlipAcrossTheSocket(t *testing.T) {
	srv, st := server(t)
	ctx := context.Background()
	at := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	for _, r := range []store.Row{
		{UUID: "a", Session: "s", Source: "f", Time: at, End: at, Words: "room a", Answer: "first", Sha: "a", Text: "a", Archived: true, Refs: []string{"ent:x"}},
		{UUID: "b", Session: "s", Source: "f", Time: at, End: at, Words: "room b", Answer: "second", Sha: "b", Text: "b", Archived: true, Refs: []string{"ent:x"}},
	} {
		if err := st.Apply(ctx, "f-"+r.UUID, []store.Row{r}, store.Cursor{}); err != nil {
			t.Fatal(err)
		}
	}
	if code, msg := post(t, srv, "/archive", `{"id":1,"archived":false}`); code != 400 || !strings.Contains(msg, "needs a why") {
		t.Fatalf("no why: %d %s", code, msg)
	}
	code, body := post(t, srv, "/archive", `{"id":1,"archived":false,"why":"checked"}`)
	var got store.Restored
	if code != 200 || json.Unmarshal([]byte(body), &got) != nil || !got.Changed || len(got.Neighbours) != 1 || got.Neighbours[0].ID != 2 || !strings.Contains(got.Neighbours[0].Line, "room b") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestKVReadStaleOneReturnsOnlyTheStaleKeys(t *testing.T) {
	srv, st := server(t)
	st.KVWrite(context.Background(), "p", map[string]string{"now/a": "x", "waits/b": "y", "fact/c": "z"}, nil)
	later := time.Now().Add(store.KVStaleAfter + time.Hour).UTC().Format(time.RFC3339) // read as they stand then: now/ and waits/ went stale
	code, body := post(t, srv, "/kv/read", `{"project":"p","prefixes":[""],"at":"`+later+`","stale":1}`)
	var got []store.KV
	if err := json.Unmarshal([]byte(body), &got); err != nil || code != 200 || len(got) != 2 || got[0].Key != "now/a" || got[1].Key != "waits/b" {
		t.Fatalf("%d %s %v", code, body, err)
	}
	if _, body = post(t, srv, "/kv/read", `{"project":"p","prefixes":[""],"at":"`+later+`"}`); strings.Count(body, `"key"`) != 3 {
		t.Fatalf("without stale every key: %s", body)
	}
	if code, _ = post(t, srv, "/kv/read", `{"project":"p","prefixes":[""],"stale":2}`); code != 400 {
		t.Fatalf("stale 2: %d", code)
	}
}
