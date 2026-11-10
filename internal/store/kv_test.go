// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "chatlog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func values(kv []KV) map[string]string {
	m := map[string]string{}
	for _, e := range kv {
		m[e.Key] = e.Value
	}
	return m
}

func TestKVReadsTheLastValueOfEachKeyAndItsPrefixes(t *testing.T) {
	ctx, s := context.Background(), open(t)
	if _, err := s.KVWrite(ctx, map[string]string{"now/jur/api": "STATE live", "now/jur/next": "WAIT M", "rule/md": "RULE md is M's"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KVWrite(ctx, map[string]string{"now/jur/api": "STATE live, image 8f06776"}, []string{"rule/md"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.KVRead(ctx, []string{"rule/md", "absent"}, []string{"now/"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if m := values(got); len(m) != 2 || m["now/jur/api"] != "STATE live, image 8f06776" || m["now/jur/next"] != "WAIT M" {
		t.Fatalf("the last value of each key, a deleted key left out: %v", m)
	}
	if got[0].Key != "now/jur/api" || got[0].Time.IsZero() {
		t.Fatalf("sorted by key, with its time: %+v", got)
	}
	all, err := s.KVRead(ctx, nil, []string{""}, time.Time{})
	if err != nil || len(all) != 2 {
		t.Fatalf(`the prefix "" is every key: %v %v`, all, err)
	}
}

func TestKVReadsTheNotesAsTheyStoodAtATime(t *testing.T) {
	ctx, s := context.Background(), open(t)
	if _, err := s.KVWrite(ctx, map[string]string{"now": "before"}, nil); err != nil {
		t.Fatal(err)
	}
	between := time.Now()
	time.Sleep(2 * time.Millisecond)
	if _, err := s.KVWrite(ctx, map[string]string{"now": "after"}, nil); err != nil {
		t.Fatal(err)
	}
	then, err := s.KVRead(ctx, []string{"now"}, nil, between)
	if err != nil || len(then) != 1 || then[0].Value != "before" {
		t.Fatalf("%v %v", then, err)
	}
}

func TestKVWriteRefusesTheWholePlanOnOneBadEntry(t *testing.T) {
	ctx, s := context.Background(), open(t)
	bad := map[string]map[string]string{
		"over the hard limit": {"a": strings.Repeat("x", KVHard+1)},
		"two lines":           {"a": "one\ntwo"},
		"empty value":         {"a": ""},
		"too deep":            {"a/b/c/d/e/f": "v"},
		"empty segment":       {"a//b": "v"},
		"space in key":        {"a b": "v"},
	}
	for name, set := range bad {
		set["ok"] = "fine"
		if _, err := s.KVWrite(ctx, set, nil); !errors.Is(err, ErrRefused) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := s.KVWrite(ctx, map[string]string{"a": "v"}, []string{"a"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("set and deleted at once: %v", err)
	}
	if got, _ := s.KVRead(ctx, nil, []string{""}, time.Time{}); len(got) != 0 {
		t.Fatalf("a refused plan wrote: %v", got)
	}
}

func TestKVWriteWarnsPastTheSoftLimitAndKeepsTheValue(t *testing.T) {
	ctx, s := context.Background(), open(t)
	long := strings.Repeat("x", KVSoft+1)
	warn, err := s.KVWrite(ctx, map[string]string{"a/b/c/d/e": long, "short": "v"}, nil)
	if err != nil || len(warn) != 1 || !strings.HasPrefix(warn[0], "a/b/c/d/e:") {
		t.Fatalf("%v %v", warn, err)
	}
	if got, _ := s.KVRead(ctx, []string{"a/b/c/d/e"}, nil, time.Time{}); len(got) != 1 || got[0].Value != long {
		t.Fatalf("kept: %v", got)
	}
	if warn, _ := s.KVWrite(ctx, map[string]string{"k": strings.Repeat("y", KVSoft)}, nil); len(warn) != 0 {
		t.Fatalf("exactly the soft limit is fine: %v", warn)
	}
}

func TestKVHasCountsADeletedKey(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, err := s.KVWrite(ctx, map[string]string{"a": "1", "b": "2"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KVWrite(ctx, nil, []string{"b"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.KVHas(ctx, []string{"a", "b", "c"})
	if err != nil || !got["a"] || !got["b"] || got["c"] || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if got, _ := s.KVHas(ctx, nil); len(got) != 0 {
		t.Fatal(got)
	}
}
