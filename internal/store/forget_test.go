// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"testing"
	"time"
)

func TestForgetSessionAndBefore(t *testing.T) {
	ctx, s := context.Background(), open(t)
	put(t, s, "p", 1, "one")   // session "s", 10:01
	put(t, s, "p", 5, "five")  // 10:05
	put(t, s, "q", 9, "other") // 10:09
	s.w.Exec(`UPDATE exchanges SET session='keep' WHERE uuid='q-9'`)
	for _, e := range []Event{
		{Time: time.Date(2026, 11, 2, 10, 2, 0, 0, time.UTC), Event: "Stop", Session: "s"},
		{Time: time.Date(2026, 11, 2, 10, 8, 0, 0, time.UTC), Event: "Stop", Session: "keep"},
	} {
		if _, err := s.AddEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := s.Export(ctx, "", func(r Row) error { n++; return nil }); err != nil || n != 3 {
		t.Fatalf("export saw %d: %v", n, err)
	}
	ids, evs, err := s.Forget(ctx, "", time.Date(2026, 11, 2, 10, 3, 0, 0, time.UTC))
	if err != nil || len(ids) != 1 || evs != 1 {
		t.Fatalf("before 10:03: %v %d %v", ids, evs, err)
	}
	ids, evs, err = s.Forget(ctx, "s", time.Time{})
	if err != nil || len(ids) != 1 || evs != 0 {
		t.Fatalf("session s: %v %d %v", ids, evs, err)
	}
	var texts []string
	s.Export(ctx, "", func(r Row) error { texts = append(texts, r.Text); return nil })
	if len(texts) != 1 || texts[0] != "other" {
		t.Fatalf("left %v", texts)
	}
	if hits, _ := s.FTS(ctx, "five", 10, Filter{}); len(hits) != 0 {
		t.Fatalf("full-text index still finds a forgotten exchange: %v", hits)
	}
	if left, _ := s.Events(ctx, "", 10); len(left) != 1 || left[0].Session != "keep" {
		t.Fatalf("events left %+v", left)
	}
}
