// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package know

import "testing"

func TestWindowOf(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  int
	}{
		{"claude-opus-5-5", 1_000_000}, {"claude-opus-4-8", 1_000_000},
		{"claude-sonnet-4-6", 1_000_000}, {"claude-sonnet-5-5", 1_000_000},
		{"claude-haiku-5-5", 1_000_000}, {"claude-fable-5-2", 1_000_000},
		{"claude-mythos-5-2", 1_000_000}, {"claude-haiku-4-5", 0},
		{"claude-sonnet-4-5", 0}, {"claude-sonnet-4-5[1m]", 1_000_000},
		{"", 0}, {"<synthetic>", 0}, {"us.anthropic.claude-opus-4-6-v1:0", 1_000_000},
		{"claude-opus-4-7[other]", 1_000_000},
	} {
		t.Run(tc.model, func(t *testing.T) {
			if got := WindowOf(tc.model); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}
