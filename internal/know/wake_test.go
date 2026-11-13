// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package know

import (
	"slices"
	"testing"
	"time"

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
	done := func(g string, at time.Time) store.KV {
		return store.KV{Key: DoneKey(g), Value: "LOADED files=9/9", Time: at}
	}
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

// review: a value that closes its own group must not break the whole-source rule (^(?:resume)|(.*)$ matches "startup")
func TestOnPatternCannotEscapeItsWholeMatchWrapper(t *testing.T) {
	ons, bad := ParseOns(kvs("sit/g/on/1", "resume)|(.*", "sit/g/on/2", "q)|(p"))
	if len(ons) != 0 || len(bad) != 2 {
		t.Fatalf("ons %v bad %v", ons, bad)
	}
}

// review: a done written in the other scope (global/sit/<g>/done) counts, the later of the two
func TestOwedTakesTheLaterOfTwoDoneRows(t *testing.T) {
	t0 := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	rows := []store.KV{armKV("a", "clear", t0),
		{Key: DoneKey("a"), Value: "y", Time: t0.Add(time.Second), Project: "global"}, // the later one first: the last row in the slice must not win
		{Key: DoneKey("a"), Value: "x", Time: t0.Add(-time.Hour)}}
	if got := Owed(rows); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

// Review (rv-correctness): armed in the project and in global is one owed situation, from the later row
func TestOwedNamesAGroupOnceWhenArmedInBothScopes(t *testing.T) {
	t0 := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	got := Owed([]store.KV{armKV("a", "clear", t0), {Key: ArmedKey("a"), Value: ArmValue("compact", t0.Add(time.Minute)), Time: t0.Add(time.Minute), Project: "global"}})
	if len(got) != 1 || got[0].Source != "compact" || !got[0].Since.Equal(t0.Add(time.Minute)) {
		t.Fatalf("%+v", got)
	}
}
