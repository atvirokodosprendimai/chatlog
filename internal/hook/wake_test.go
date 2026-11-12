// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package hook

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// wakeRig has the wake situation as M's notes would hold it: global, so every project has it.
func wakeRig(t *testing.T) *rig {
	r := newRig(t)
	r.kvIn("global", map[string]string{"sit/醒": "wake | load the whole list", "sit/醒/on/1": "startup|compact|clear"})
	return r
}

func (r *rig) start(cwd, source string) string {
	r.t.Helper()
	out, err := r.run("SessionStart", fmt.Sprintf(`{"session_id":"s1","cwd":%q,"source":%q}`, cwd, source))
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

func (r *rig) done(project, line string) {
	r.t.Helper()
	r.kvIn(project, map[string]string{"sit/醒/done": line})
}

func TestAWakeEventArmsAndEveryPromptCarriesTheReminderUntilDone(t *testing.T) {
	r := wakeRig(t)
	if got := r.promptIn("/u/a", "hello there"); strings.Contains(got, "WAKE") {
		t.Fatalf("nothing armed, nothing said: %q", got)
	}
	r.start("/u/a", "compact")
	for _, p := range []string{"M's prompt", "a cron tick", "/loop 5m check"} {
		got := r.promptIn("/u/a", p)
		for _, w := range []string{"WAKE NOT DONE", "chatlog wake --dump", "kv_write sit/醒/done", "situation sit/醒 wake | load the whole list"} {
			if !strings.Contains(got, w) {
				t.Fatalf("prompt %q: %q missing from\n%s", p, w, got)
			}
		}
	}
	r.done("path:/u/a", "LOADED files=9/9")
	if got := r.promptIn("/u/a", "after done"); strings.Contains(got, "WAKE") || strings.Contains(got, "situation sit/醒") {
		t.Fatalf("done stops it: %q", got)
	}
	r.start("/u/a", "clear") // a second event owes it again
	if got := r.promptIn("/u/a", "after clear"); !strings.Contains(got, "WAKE NOT DONE (clear") {
		t.Fatalf("re-armed: %q", got)
	}
}

func TestOnlyTheEventsThePatternNamesArm(t *testing.T) {
	r := wakeRig(t)
	r.start("/u/a", "resume")
	if got := r.promptIn("/u/a", "x"); strings.Contains(got, "WAKE") {
		t.Fatalf("resume does not match startup|compact|clear: %q", got)
	}
	r.start("/u/a", "startup")
	if got := r.promptIn("/u/a", "x"); !strings.Contains(got, "WAKE NOT DONE (startup") {
		t.Fatalf("startup: %q", got)
	}
}

func TestTheReminderIsOneProjectsOwn(t *testing.T) {
	r := wakeRig(t)
	r.start("/u/a", "compact")
	if got := r.promptIn("/u/b", "x"); strings.Contains(got, "WAKE") {
		t.Fatalf("armed for a, said in b: %q", got)
	}
	if got := r.promptIn("/u/a", "x"); !strings.Contains(got, "WAKE NOT DONE") {
		t.Fatalf("a: %q", got)
	}
}

func TestASituationWithoutOnKeysIsNeverArmedAndStaysPerMatch(t *testing.T) {
	r := newRig(t)
	r.kv("sit/rel", "release | gate first", "sit/rel/words/1", "release")
	r.start("", "compact")
	got, err := r.d.Store.KVRead(context.Background(), "unknown", nil, []string{"sit/"}, r.now)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		if strings.HasSuffix(e.Key, "/armed") {
			t.Fatalf("armed a situation with no on/: %s", e.Key)
		}
	}
	if out, _ := r.prompt("s1", "release it", ""); !strings.Contains(out, "situation sit/rel") {
		t.Fatalf("per match: %q", out)
	}
	if out, _ := r.prompt("s1", "something else", ""); strings.Contains(out, "situation sit/rel") {
		t.Fatalf("not sticky: %q", out)
	}
}

func TestTheReminderFitsAndTheSessionStartDigestStays(t *testing.T) {
	r := wakeRig(t)
	if out := r.start("/u/a", "compact"); !strings.Contains(out, "CHATLOG") {
		t.Fatalf("the digest is still the SessionStart text: %q", out)
	}
	out := r.promptIn("/u/a", "x")
	i := strings.Index(out, "WAKE NOT DONE")
	if i < 0 {
		t.Fatalf("no reminder: %q", out)
	}
	line, _, _ := strings.Cut(out[i:], "\n")
	if len(line) > 400 {
		t.Fatalf("the reminder is %d bytes", len(line))
	}
}

func TestAnEventPatternThatDoesNotCompileIsSaidNotRaised(t *testing.T) {
	r := newRig(t)
	r.kv("sit/w", "wake | x", "sit/w/on/1", "(")
	r.start("", "compact")
	if out, _ := r.prompt("s1", "x", ""); !strings.Contains(out, "sit/w/on/1") {
		t.Fatalf("the bad key is named: %q", out)
	}
}

func TestPromptPieceOffSilencesTheWake(t *testing.T) {
	r := wakeRig(t)
	r.off("prompt")
	r.start("/u/a", "compact")
	if out := r.promptIn("/u/a", "x"); strings.Contains(out, "WAKE") {
		t.Fatalf("prompt piece off: %q", out)
	}
	r.off()
	if out := r.promptIn("/u/a", "x"); strings.Contains(out, "WAKE") {
		t.Fatalf("armed while the piece was off? the event came while it was off: %q", out)
	}
}
