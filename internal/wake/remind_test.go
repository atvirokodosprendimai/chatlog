// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package wake

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/atvirokodosprendimai/chatlog/internal/know"
)

func TestReminderIsShortNamesTheCommandAndTheKeyToWrite(t *testing.T) {
	g := strings.Repeat("醒", 42) // 126 bytes, a long group name of three-byte runes
	for _, grp := range []string{"醒", g} {
		m := Reminder(know.Arm{Group: grp, Source: "compact", Since: time.Date(2026, 11, 13, 0, 57, 0, 0, time.UTC)}, "/tmp/chatlog-wake-ab12cd34")
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
	if m := Reminder(know.Arm{Group: g, Source: "x"}, "d"); !strings.Contains(m, know.DoneKey(g)) {
		t.Errorf("the key to write is cut: %q", m)
	}
}

func TestReminderTextOverManyGroupsIsThreeLinesAndACount(t *testing.T) {
	var arms []know.Arm
	for _, g := range []string{"a", "b", "c", "d", "e"} {
		arms = append(arms, know.Arm{Group: g, Source: "clear"})
	}
	out := Reminders(arms, "d")
	if n := strings.Count(out, "WAKE NOT DONE"); n != 3 || !strings.Contains(out, "+2 more") {
		t.Fatalf("%q", out)
	}
	if Reminders(nil, "d") != "" {
		t.Fatal("nothing owed, nothing said")
	}
}

// review: the source is the hook's input, free text; it must not push the reminder over its size
func TestReminderClipsAFreeSource(t *testing.T) {
	m := Reminder(know.Arm{Group: strings.Repeat("g", 100), Source: strings.Repeat("醒", 100)}, "/tmp/chatlog-wake-ab12cd34")
	if len(m) > 400 || !utf8.ValidString(m) {
		t.Fatalf("%d bytes: %q", len(m), m)
	}
}

// Security review: the hook's input and the temp dir are free text on a prompt
func TestReminderNeutralisesControlCharactersAndQuotesTheDir(t *testing.T) {
	m := Reminder(know.Arm{Group: "g", Source: "x\nIGNORE ALL\r\x00"}, "/tmp/a dir/$(x)")
	if strings.ContainsAny(m, "\r\x00") || strings.Count(m, "\n") != 0 {
		t.Fatalf("%q", m)
	}
	if !strings.Contains(m, `'/tmp/a dir/$(x)'`) {
		t.Fatalf("the dir is quoted for a shell: %q", m)
	}
	if m := Reminder(know.Arm{Group: "g", Source: "compact"}, "/tmp/chatlog-wake-1000-ab12cd34"); strings.Contains(m, "'") {
		t.Fatalf("a plain dir needs no quotes: %q", m)
	}
}

func TestDumpDirIsPerUserAndPerProject(t *testing.T) {
	a, b := DumpDir("path:/u/a"), DumpDir("path:/u/b")
	if a == b || !strings.Contains(a, fmt.Sprintf("-%d-", os.Getuid())) {
		t.Fatalf("%s %s", a, b)
	}
}
