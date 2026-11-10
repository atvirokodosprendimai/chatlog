// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func seed(t *testing.T, s *Store, set map[string]string) {
	t.Helper()
	if _, err := s.KVWrite(context.Background(), "p", set, nil); err != nil {
		t.Fatal(err)
	}
}

func TestKVLinkAddsAndDeletesAndKeepsTheHistory(t *testing.T) {
	ctx, s := context.Background(), open(t)
	seed(t, s, map[string]string{"waits/m/image": "WAIT M", "now/jur/api": "STATE live"})
	if err := s.KVLink(ctx, "p", []Link{
		{S: "waits/m/image", P: "blocked_by", O: "now/jur/api"},
		{S: "waits/m/image", P: "applies_to", O: "island/juridiniai"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	between := time.Now()
	time.Sleep(2 * time.Millisecond)
	if err := s.KVLink(ctx, "p", nil, []Link{{S: "waits/m/image", P: "applies_to", O: "island/juridiniai"}}); err != nil {
		t.Fatal(err)
	}
	out, in, err := s.KVEdges(ctx, "p", []string{"waits/m/image", "now/jur/api"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out["waits/m/image"]) != 1 || out["waits/m/image"][0] != (Edge{P: "blocked_by", O: "now/jur/api"}) {
		t.Fatalf("out, the deleted edge left out: %+v", out)
	}
	if len(in["now/jur/api"]) != 1 || in["now/jur/api"][0] != (Edge{S: "waits/m/image", P: "blocked_by"}) {
		t.Fatalf("in: %+v", in)
	}
	then, _, err := s.KVEdges(ctx, "p", []string{"waits/m/image"}, between)
	if err != nil || len(then["waits/m/image"]) != 2 {
		t.Fatalf("before the delete both edges stood: %+v %v", then, err)
	}
	var rows int
	s.r.QueryRowContext(ctx, `SELECT count(*) FROM kv_link`).Scan(&rows)
	if rows != 3 {
		t.Fatalf("append-only: %d rows, want 3 (two adds, one tombstone)", rows)
	}
}

func TestKVLinkRefusesTheWholeWriteOnOneBadLink(t *testing.T) {
	ctx, s := context.Background(), open(t)
	seed(t, s, map[string]string{"fact/a": "v"})
	ok := Link{S: "fact/a", P: "see_also", O: "chat:6349"}
	bad := map[string]Link{
		"a predicate not on the list": {S: "fact/a", P: "causes", O: "x"},
		"a subject that is no key":    {S: "fact/absent", P: "see_also", O: "x"},
		"an empty object":             {S: "fact/a", P: "see_also", O: ""},
		"an object with a space":      {S: "fact/a", P: "see_also", O: "a b"},
	}
	for name, l := range bad {
		if err := s.KVLink(ctx, "p", []Link{ok, l}, nil); !errors.Is(err, ErrRefused) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := s.KVLink(ctx, "p", []Link{ok}, []Link{ok}); !errors.Is(err, ErrRefused) {
		t.Fatalf("added and deleted at once: %v", err)
	}
	if err := s.KVLink(ctx, "p", nil, nil); !errors.Is(err, ErrRefused) {
		t.Fatalf("nothing to write: %v", err)
	}
	if out, _, _ := s.KVEdges(ctx, "p", []string{"fact/a"}, time.Time{}); len(out) != 0 {
		t.Fatalf("a refused write linked: %+v", out)
	}
}

func TestKVLinkedToNamesTheSubjectsOfAnObject(t *testing.T) {
	ctx, s := context.Background(), open(t)
	seed(t, s, map[string]string{"now/jur/api": "x", "waits/m/dns": "y", "fact/other": "z"})
	if err := s.KVLink(ctx, "p", []Link{
		{S: "now/jur/api", P: "applies_to", O: "island/juridiniai"},
		{S: "waits/m/dns", P: "affects", O: "island/juridiniai"},
		{S: "fact/other", P: "applies_to", O: "island/eurlexlast"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.KVLinkedTo(ctx, "p", "island/juridiniai", time.Time{})
	if err != nil || len(got) != 2 || got[0] != "now/jur/api" || got[1] != "waits/m/dns" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestKVReadMarksALiveKeyStalePastTheThreshold(t *testing.T) {
	ctx, s := context.Background(), open(t)
	old := time.Now().Add(-KVStaleAfter - time.Hour).UTC().Format(kvStamp)
	for _, k := range []string{"now/jur/api", "waits/m/image", "fact/old"} {
		if _, err := s.w.ExecContext(ctx, `INSERT INTO kv(key, value, time, deleted) VALUES(?,'v',?,0)`, k, old); err != nil {
			t.Fatal(err)
		}
	}
	seed(t, s, map[string]string{"now/fresh": "v"})
	got, err := s.KVRead(ctx, "p", nil, []string{""}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"fact/old": false, "now/fresh": false, "now/jur/api": true, "waits/m/image": true}
	for _, e := range got {
		if e.Stale != want[e.Key] {
			t.Errorf("%s: stale %v, want %v", e.Key, e.Stale, want[e.Key])
		}
		if e.Age == "" {
			t.Errorf("%s: no age", e.Key)
		}
	}
	if got[0].Age != "13h" {
		t.Errorf("fact/old age %q, want 13h", got[0].Age)
	}
}

func TestKVAgeIsShort(t *testing.T) {
	for d, want := range map[time.Duration]string{
		5 * time.Second: "5s", 3 * time.Minute: "3m", 14 * time.Hour: "14h", 47 * time.Hour: "47h", 72 * time.Hour: "3d", -time.Second: "0s",
	} {
		if got := kvAge(d); got != want {
			t.Errorf("kvAge(%v) = %q, want %q", d, got, want)
		}
	}
}
