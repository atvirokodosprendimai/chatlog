// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package eval scores the retrieval variants against a file of real questions: `chatlog eval FILE`.
// The expected exchanges are fixed in the file before any run: this package only counts.
package eval

import (
	"sort"
	"time"
)

// Question is one line of the eval file.
type Question struct {
	Q      string   `json:"q"`
	Expect []string `json:"expect"` // uuids of the exchanges that answer it
	Why    string   `json:"why"`
}

// Rank is the 1-based position of the first expected id, 0 when absent.
func Rank(got, expect []string) int {
	for i, g := range got {
		for _, e := range expect {
			if g == e {
				return i + 1
			}
		}
	}
	return 0
}

// Score collects ranks and times of one variant.
type Score struct {
	ranks []int
	times []time.Duration
}

// Add records one question's rank and time.
func (s *Score) Add(rank int, took time.Duration) {
	s.ranks = append(s.ranks, rank)
	s.times = append(s.times, took)
}

// Result is what a variant scored.
type Result struct {
	Hit1, Hit5, MRR float64
	P95             time.Duration
}

// Result computes the metrics.
func (s *Score) Result() Result {
	var r Result
	if len(s.ranks) == 0 {
		return r
	}
	for _, k := range s.ranks {
		if k == 1 {
			r.Hit1++
		}
		if k >= 1 && k <= 5 {
			r.Hit5++
		}
		if k > 0 {
			r.MRR += 1 / float64(k)
		}
	}
	n := float64(len(s.ranks))
	r.Hit1, r.Hit5, r.MRR = r.Hit1/n, r.Hit5/n, r.MRR/n
	t := append([]time.Duration(nil), s.times...)
	sort.Slice(t, func(i, j int) bool { return t[i] < t[j] })
	r.P95 = t[min(len(t)-1, (len(t)*95)/100)]
	return r
}
