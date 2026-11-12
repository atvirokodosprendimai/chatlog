package store

import (
	"testing"
	"time"
)

func TestParseWhenSpansAndTimes(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	ok := []struct {
		in      string
		forward bool
		want    time.Time
	}{
		{"1h", true, now.Add(time.Hour)},
		{"1h", false, now.Add(-time.Hour)},
		{"90m", false, now.Add(-90 * time.Minute)},
		{"7d", true, now.Add(7 * 24 * time.Hour)},
		{"7d", false, now.Add(-7 * 24 * time.Hour)},
		{"2026-11-12T10:00:00+02:00", false, time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)},
		{"36500d", false, now.Add(-36500 * 24 * time.Hour)},
	}
	for _, c := range ok {
		got, err := ParseWhen(c.in, now, c.forward)
		if err != nil || !got.Equal(c.want) {
			t.Errorf("%q forward=%v: %v %v, want %v", c.in, c.forward, got, err, c.want)
		}
	}
	bad := []struct {
		in      string
		forward bool
	}{
		{"", true}, {"yesterday", false}, {"d", false}, {"xd", false}, {"1.5d", false},
		{"-1h", false}, {"-1d", false}, {"0", false}, {"0s", false}, {"0d", false}, // a span back from now is positive
		{"36501d", false}, {"200000d", false}, {"99999999999d", true}, // past 100 years: it would wrap, not mean it
	}
	for _, c := range bad {
		if got, err := ParseWhen(c.in, now, c.forward); err == nil {
			t.Errorf("%q forward=%v: accepted as %v", c.in, c.forward, got)
		}
	}
	if _, err := ParseWhen("0s", now, true); err != nil {
		t.Errorf("forward, a zero span is parsed and kvExpiry says it is not in the future, as before: %v", err)
	}
}
