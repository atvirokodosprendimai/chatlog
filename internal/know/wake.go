// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package know

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
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

// maxReminders is how many owed situations a prompt names; the rest are counted.
const maxReminders = 3

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
			done[e.Key] = e.Time
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

// Reminder is the line an owed situation puts on a prompt: short, naming the command that loads and the key that finishes.
func Reminder(a Arm, dir string) string {
	return fmt.Sprintf("WAKE NOT DONE (%s, since %s): run chatlog wake --dump %s, read EVERY page in ONE turn in parallel, then kv_write %s = its LOADED line and tell the user it. Repeats each prompt until done.",
		a.Source, a.Since.UTC().Format(time.RFC3339), dir, DoneKey(a.Group))
}

// Reminders is the lines for the owed situations, the first three, then a count of the rest; "" when nothing is owed.
func Reminders(arms []Arm, dir string) string {
	var lines []string
	for i, a := range arms {
		if i == maxReminders {
			lines = append(lines, fmt.Sprintf("(+%d more owed: kv_read prefix sit/ and look for /armed)", len(arms)-maxReminders))
			break
		}
		lines = append(lines, Reminder(a, dir))
	}
	return strings.Join(lines, "\n")
}

// DumpDir is where `chatlog wake --dump` puts a project's pages, and where the reminder tells the session to look: one per
// project, so two sessions waking at once do not overwrite each other's pages.
func DumpDir(project string) string {
	sum := sha1.Sum([]byte(project))
	return filepath.Join(os.TempDir(), "chatlog-wake-"+hex.EncodeToString(sum[:4]))
}
