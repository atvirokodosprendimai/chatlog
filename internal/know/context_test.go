// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package know

import (
	"strings"
	"testing"
)

func TestMeter(t *testing.T) {
	for _, c := range []struct {
		name         string
		used, window int
		sleep        float64
		want, not    string
	}{
		{"nothing used", 0, 200000, 0.6, "", ""},
		{"no window", 5, 0, 0.6, "", ""},
		{"under the sleep", 50_000, 200_000, 0.6, "context: 50k of ~200k (25%), ~150k left; sleep at 60% (~120k).", "SLEEP"},
		{"one token under", 119_999, 200_000, 0.6, "context: 119k of ~200k (60%)", "SLEEP"},
		{"at the sleep", 120_000, 200_000, 0.6, "SLEEP NOW, context: 120k of ~200k (60%), ~80k left", ""},
		{"past the window", 250_000, 200_000, 0.6, "~0k left", ""},
		{"a larger window and another share", 600_000, 1_000_000, 0.5, "SLEEP NOW, context: 600k of ~1000k (60%), ~400k left; sleep at 50% (~500k)", ""},
	} {
		got := Meter(c.used, c.window, c.sleep)
		if !strings.Contains(got, c.want) || (c.not != "" && strings.Contains(got, c.not)) {
			t.Errorf("%s: %q, want %q without %q", c.name, got, c.want, c.not)
		}
	}
	if !strings.Contains(Meter(130_000, 200_000, 0.6), "kv_write the pointers") {
		t.Error("the sleep line says what to do")
	}
}
