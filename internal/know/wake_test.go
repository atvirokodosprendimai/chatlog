// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package know

import (
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

func TestOnMatchesTheWholeSessionStartSource(t *testing.T) {
	ons, bad := ParseOns(kvs("sit/醒/on/1", "startup|compact|clear", "sit/x/on/1", "(", "sit/y/words/1", "clear"))
	if len(ons) != 1 || !slices.Equal(bad, []string{"sit/x/on/1"}) {
		t.Fatalf("ons %v bad %v", ons, bad)
	}
	for _, c := range []struct {
		src  string
		want []string
	}{{"startup", []string{"醒"}}, {"compact", []string{"醒"}}, {"clear", []string{"醒"}}, {"resume", nil}, {"", nil}, {"startupx", nil}, {"xcompact", nil}} {
		if got := MatchOns(ons, c.src); !slices.Equal(got, c.want) {
			t.Errorf("source %q: %v, want %v", c.src, got, c.want)
		}
	}
}

func armKV(g, src string, at time.Time) store.KV {
	return store.KV{Key: ArmedKey(g), Value: ArmValue(src, at), Time: at}
}

func TestOwedIsArmedWithoutALaterDone(t *testing.T) {
	t0 := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	done := func(g string, at time.Time) store.KV { return store.KV{Key: DoneKey(g), Value: "LOADED files=9/9", Time: at} }
	for _, c := range []struct {
		name string
		kvs  []store.KV
		want []string
	}{
		{"armed, never done", []store.KV{armKV("a", "compact", t0)}, []string{"a"}},
		{"done before the arm: a second event re-arms", []store.KV{armKV("a", "clear", t0), done("a", t0.Add(-time.Second))}, []string{"a"}},
		{"done at the same stamp is not later", []store.KV{armKV("a", "clear", t0), done("a", t0)}, []string{"a"}},
		{"done after", []store.KV{armKV("a", "clear", t0), done("a", t0.Add(time.Nanosecond))}, nil},
		{"one group's done does not finish another", []store.KV{armKV("a", "clear", t0), armKV("b", "clear", t0), done("a", t0.Add(time.Second))}, []string{"b"}},
		{"done alone is nothing", []store.KV{done("a", t0)}, nil},
		{"not armed keys are ignored", []store.KV{{Key: "sit/a/armed/x", Value: "compact x", Time: t0}, {Key: "sit/a", Value: "what", Time: t0}}, nil},
	} {
		var got []string
		for _, a := range Owed(c.kvs) {
			got = append(got, a.Group)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	a := Owed([]store.KV{armKV("a", "compact", t0)})[0]
	if a.Source != "compact" || !a.Since.Equal(t0) {
		t.Errorf("arm: %+v", a)
	}
}

func TestReminderIsShortNamesTheCommandAndTheKeyToWrite(t *testing.T) {
	g := strings.Repeat("醒", 42) // 126 bytes, a long group name of three-byte runes
	for _, grp := range []string{"醒", g} {
		m := Reminder(Arm{Group: grp, Source: "compact", Since: time.Date(2026, 11, 13, 0, 57, 0, 0, time.UTC)}, "/tmp/chatlog-wake-ab12cd34")
		if len(m) > 400 || !utf8.ValidString(m) {
			t.Fatalf("%d bytes, valid %v: %q", len(m), utf8.ValidString(m), m)
		}
		for _, w := range []string{"WAKE NOT DONE", "chatlog wake --dump /tmp/chatlog-wake-ab12cd34", "ONE", "parallel", "/done", "2026-11-13T00:57:00Z", "compact"} {
			if !strings.Contains(m, w) {
				t.Errorf("%q missing from %q", w, m)
			}
		}
	}
	// the key to write is the whole key, not the clipped one
	if m := Reminder(Arm{Group: g, Source: "x"}, "d"); !strings.Contains(m, DoneKey(g)) {
		t.Errorf("the key to write is cut: %q", m)
	}
}

func TestReminderTextOverManyGroupsIsThreeLinesAndACount(t *testing.T) {
	var arms []Arm
	for _, g := range []string{"a", "b", "c", "d", "e"} {
		arms = append(arms, Arm{Group: g, Source: "clear"})
	}
	out := Reminders(arms, "d")
	if n := strings.Count(out, "WAKE NOT DONE"); n != 3 || !strings.Contains(out, "+2 more") {
		t.Fatalf("%q", out)
	}
	if Reminders(nil, "d") != "" {
		t.Fatal("nothing owed, nothing said")
	}
}
