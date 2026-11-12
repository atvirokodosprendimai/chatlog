// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package know

import (
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

// An event situation (docs/wake.md): sit/<g>/on/<n> is a regex that must match the whole SessionStart source. Firing writes
// sit/<g>/armed into the project's notes; from then the situation is owed on every prompt until sit/<g>/done is written
// later than it. A situation without an on/ key is not sticky and has no such state.
var (
	onKey    = regexp.MustCompile(`^sit/([^/]+)/on/[^/]+$`)
	armedKey = regexp.MustCompile(`^sit/([^/]+)/armed$`)
)

// On is one event pattern of a situation.
type On struct {
	Group string
	RX    *regexp.Regexp
}

// Arm is an owed situation: the source of the event that armed it and when.
type Arm struct {
	Group  string
	Source string
	Since  time.Time
}

// ArmedKey and DoneKey are the two keys of a situation's owed state.
func ArmedKey(group string) string { return SitPrefix + group + "/armed" }
func DoneKey(group string) string  { return SitPrefix + group + "/done" }

// ArmValue is what the hook writes to the armed key: the source and the time, to the nanosecond, so a second event with
// the same source is a new value.
func ArmValue(source string, at time.Time) string {
	return source + " " + at.UTC().Format(time.RFC3339Nano)
}

// ParseOns reads the event patterns out of notes. A pattern must match the whole source; bad as in ParseSigs.
func ParseOns(kvs []store.KV) (ons []On, bad []string) {
	for _, e := range kvs {
		m := onKey.FindStringSubmatch(e.Key)
		if m == nil {
			continue
		}
		_, alone := regexp.Compile(e.Value) // alone first: a value that closes its own group would escape the whole-source wrapper
		rx, err := regexp.Compile(`^(?:` + e.Value + `)$`)
		if alone != nil || err != nil || strings.TrimSpace(e.Value) == "" {
			bad = append(bad, e.Key)
			continue
		}
		ons = append(ons, On{m[1], rx})
	}
	return ons, bad
}

// MatchOns returns the groups with a pattern that fits the source, each once, in key order.
func MatchOns(ons []On, source string) []string {
	var hit []string
	for _, o := range ons {
		if !slices.Contains(hit, o.Group) && o.RX.MatchString(source) {
			hit = append(hit, o.Group)
		}
	}
	return hit
}

// Owed returns the armed situations that no later done finished, in key order. Later is by the stamps the store gave the
// two writes, so two machines' clocks are never compared; the same stamp is not later.
func Owed(kvs []store.KV) []Arm {
	done := map[string]time.Time{}
	for _, e := range kvs {
		if strings.HasPrefix(e.Key, SitPrefix) && strings.HasSuffix(e.Key, "/done") {
			done[e.Key] = laterOf(done[e.Key], e.Time) // a done of the project and one of global (the other scope) both count
		}
	}
	var out []Arm
	for _, e := range kvs {
		m := armedKey.FindStringSubmatch(e.Key)
		if m == nil {
			continue
		}
		if d, ok := done[DoneKey(m[1])]; ok && d.After(e.Time) {
			continue
		}
		src, _, _ := strings.Cut(e.Value, " ")
		out = append(out, Arm{Group: m[1], Source: src, Since: e.Time})
	}
	return out
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
