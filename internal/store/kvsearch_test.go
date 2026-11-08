// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"testing"
)

func TestKVPendingIsTheLastValueOfEachLiveKeyNotYetEmbedded(t *testing.T) {
	ctx, s := context.Background(), open(t)
	if _, err := s.KVWrite(ctx, map[string]string{"fact/kv/frame": "RULE old", "fact/jur/api": "STATE live", "now/x": "gone soon"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KVWrite(ctx, map[string]string{"fact/kv/frame": "RULE frame"}, []string{"now/x"}); err != nil {
		t.Fatal(err)
	}
	p, err := s.KVPending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range p {
		got[e.Text] = true
	}
	if len(p) != 2 || !got["fact/kv/frame: RULE frame"] || !got["fact/jur/api: STATE live"] {
		t.Fatalf("pending %+v: want the two live keys at their last value", p)
	}
	if err := s.KVMarkEmbedded(ctx, p); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.KVPending(ctx, 10); len(p) != 0 {
		t.Fatalf("pending after marking: %+v", p)
	}
}

func TestKVLatestDropsOldVersionsDeletedKeysAndOtherPrefixes(t *testing.T) {
	ctx, s := context.Background(), open(t)
	s.KVWrite(ctx, map[string]string{"fact/kv/frame": "RULE old", "fact/jur/api": "STATE live", "now/x": "x"}, nil)
	old, _ := s.KVPending(ctx, 10)
	s.KVWrite(ctx, map[string]string{"fact/kv/frame": "RULE frame"}, []string{"now/x"})
	cur, _ := s.KVPending(ctx, 10)
	var rids []int64
	for _, e := range append(old, cur...) {
		rids = append(rids, e.ID)
	}
	got, err := s.KVLatest(ctx, rids, "fact/kv/")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v: want only fact/kv/frame at its last value", got)
	}
	for _, e := range got {
		if e.Key != "fact/kv/frame" || e.Value != "RULE frame" {
			t.Fatalf("got %+v", e)
		}
	}
}
