// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package eval

import (
	"math"
	"testing"
	"time"
)

func TestScore(t *testing.T) {
	var s Score
	s.Add(1, 10*time.Millisecond) // rank 1
	s.Add(3, 20*time.Millisecond)
	s.Add(0, 30*time.Millisecond) // not found
	s.Add(6, 40*time.Millisecond) // past 5
	r := s.Result()
	if r.Hit1 != 0.25 || r.Hit5 != 0.5 {
		t.Fatalf("%+v", r)
	}
	if math.Abs(r.MRR-(1+1.0/3+1.0/6)/4) > 1e-9 {
		t.Fatalf("%+v", r)
	}
	if r.P95 != 40*time.Millisecond {
		t.Fatalf("%+v", r)
	}
}

func TestRank(t *testing.T) {
	if got := Rank([]string{"a", "b", "c"}, []string{"x", "b"}); got != 2 {
		t.Fatal(got)
	}
	if got := Rank([]string{"a"}, []string{"x"}); got != 0 {
		t.Fatal(got)
	}
}
