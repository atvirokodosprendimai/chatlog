// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/project"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

// wakeHome is a new machine with chatlog installed: a data dir of its own, a file or two, and the list loaded.
func wakeHome(t *testing.T) (*home, []string) {
	t.Helper()
	h := newHome(t)
	h.write("docs/a.md", "one\ntwo\nthree\n")
	h.write("list.txt", strings.Join([]string{
		"# the wake of this machine",
		"file " + h.path("docs/a.md"),
		"file ~/nowhere.md",
		"prefix rule/",
		"prefix known/",
		"step call the skill adr-write",
		"note sit/醒 wake | load the whole list with chatlog wake --dump",
		"note sit/醒/on/1 startup|compact|clear",
	}, "\n")+"\n")
	return h, []string{"--data-dir", h.path("d")}
}

func (h *home) chatlog(data []string, stdin string, args ...string) (string, error) {
	h.t.Helper()
	return h.run(stdin, append(append([]string{}, data...), args...)...)
}

func TestWakeLoadSeedsTheListAndWakePrintsIt(t *testing.T) {
	h, d := wakeHome(t)
	if out, err := h.chatlog(d, "", "wake", "load", h.path("list.txt")); err != nil || !strings.Contains(out, "file/01") {
		t.Fatalf("load: %v\n%s", err, out)
	}
	out, err := h.chatlog(d, "", "wake")
	if err != nil {
		t.Fatalf("wake: %v\n%s", err, out)
	}
	for _, w := range []string{"     3  " + h.path("docs/a.md"), "MISSING " + h.path("nowhere.md"), "total lines: 3", "rule/ (0 notes)", "  call the skill adr-write",
		"LOADED files=1/2 lines=3 kv rule=0 known=0; skipped: " + h.path("nowhere.md")} {
		if !strings.Contains(out, w) {
			t.Errorf("%q missing from\n%s", w, out)
		}
	}
	// the list is replaced, not added to: a second load with one line leaves one line
	h.write("list2.txt", "step only this\n")
	if out, err := h.chatlog(d, "", "wake", "load", h.path("list2.txt")); err != nil || !strings.Contains(out, "delete") {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, _ := h.chatlog(d, "", "wake"); strings.Contains(out, "a.md") || !strings.Contains(out, "only this") {
		t.Fatalf("not replaced:\n%s", out)
	}
	// the situation of the first file stays: load only deletes under global/wake/
	st, err := store.Open(h.path("d/chatlog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if kvs, _ := st.KVRead(context.Background(), "global", nil, []string{"sit/醒"}, timeZero); len(kvs) != 2 {
		t.Fatalf("the notes a list file wrote stay: %v", kvs)
	}
}

func TestWakeLoadRefusesABadLineAndWritesNothing(t *testing.T) {
	h, d := wakeHome(t)
	h.write("bad.txt", "step fine\nfile\nwhat is this\n")
	if out, err := h.chatlog(d, "", "wake", "load", h.path("bad.txt")); err == nil || !strings.Contains(out, "line 2") {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, _ := h.chatlog(d, "", "wake"); strings.Contains(out, "== STEPS") {
		t.Fatalf("a refused file wrote:\n%s", out)
	}
	if out, err := h.chatlog(d, "step from stdin\n", "wake", "load", "-"); err != nil {
		t.Fatalf("stdin: %v\n%s", err, out)
	}
}

func TestWakeWithoutAListIsTheDigestAsBefore(t *testing.T) {
	h, d := wakeHome(t)
	if out, err := h.chatlog(d, "", "wake"); err != nil || !strings.Contains(out, "CHATLOG WAKE") {
		t.Fatalf("%v\n%s", err, out)
	}
	h.chatlog(d, "", "wake", "load", h.path("list.txt"))
	for _, args := range [][]string{{"wake", "--digest"}, {"wake", "--kind", "compact"}, {"wake", "--bytes", "9000"}} {
		if out, err := h.chatlog(d, "", args...); err != nil || !strings.Contains(out, "CHATLOG WAKE") {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
}

func TestWakeDumpWritesPagesAndSaysTheCounts(t *testing.T) {
	h, d := wakeHome(t)
	h.write("docs/a.md", strings.Repeat("a line of a long file\n", 3000)) // 66000 bytes
	h.chatlog(d, "", "wake", "load", h.path("list.txt"))
	dir := h.path("pages")
	out, err := h.chatlog(d, "", "wake", "--dump", dir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pages, _ := filepath.Glob(filepath.Join(dir, "page-*.txt"))
	if len(pages) != 3 {
		t.Fatalf("pages %v\n%s", pages, out)
	}
	var all strings.Builder
	for _, p := range pages {
		b, _ := os.ReadFile(p)
		if len(b) > 24000 {
			t.Errorf("%s is %d bytes", p, len(b))
		}
		if !strings.Contains(out, p) {
			t.Errorf("the page %s is not listed:\n%s", p, out)
		}
		all.Write(b)
	}
	if !strings.Contains(all.String(), "=== FILE "+h.path("docs/a.md")+" (3000 lines)") || !strings.Contains(all.String(), "=== MISSING "+h.path("nowhere.md")) {
		t.Errorf("the content: %.300s", all.String())
	}
	for _, w := range []string{"LOADED files=1/2 lines=3000", "ONE turn", "parallel"} {
		if !strings.Contains(out, w) {
			t.Errorf("%q missing from\n%s", w, out)
		}
	}
}

func TestTheWholeWakeThroughTheHooks(t *testing.T) {
	h, d := wakeHome(t)
	h.chatlog(d, "", "wake", "load", h.path("list.txt"))
	hook := func(event, js string) string {
		out, err := h.chatlog(d, js, "hook", event)
		if err != nil {
			t.Fatalf("%s: %v\n%s", event, err, out)
		}
		return out
	}
	in := func(extra string) string { return fmt.Sprintf(`{"session_id":"s1","cwd":%q%s}`, h.dir, extra) }
	if out := hook("UserPromptSubmit", in(`,"prompt":"first"`)); strings.Contains(out, "WAKE NOT DONE") {
		t.Fatalf("nothing armed yet: %s", out)
	}
	hook("SessionStart", in(`,"source":"compact"`))
	out := hook("UserPromptSubmit", in(`,"prompt":"a cron tick"`))
	for _, w := range []string{"WAKE NOT DONE", "chatlog wake --dump", "kv_write sit/醒/done", "situation sit/醒 wake"} {
		if !strings.Contains(out, w) {
			t.Fatalf("%q missing from %s", w, out)
		}
	}
	// the owed wake is named by `chatlog wake` too
	if o, _ := h.chatlog(d, "", "wake"); !strings.Contains(o, `kv_write sit/醒/done = "<that LOADED line>"`) {
		t.Errorf("wake does not name the key:\n%s", o)
	}
	st, err := store.Open(h.path("d/chatlog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := project.Resolve(h.dir, st).Key
	if _, err := st.KVWrite(context.Background(), key, map[string]string{"sit/醒/done": "LOADED files=1/2"}, nil); err != nil {
		t.Fatal(err)
	}
	if out := hook("UserPromptSubmit", in(`,"prompt":"after done"`)); strings.Contains(out, "WAKE NOT DONE") || strings.Contains(out, "situation sit/醒") {
		t.Fatalf("done, and still said: %s", out)
	}
	hook("SessionStart", in(`,"source":"clear"`))
	if out := hook("UserPromptSubmit", in(`,"prompt":"after clear"`)); !strings.Contains(out, "WAKE NOT DONE (clear") {
		t.Fatalf("a second event re-arms: %s", out)
	}
}

var timeZero time.Time

// Review (rv-delivery, rv-compat): a load of nothing must not wipe the list unasked
func TestWakeLoadOfNothingIsRefusedUnlessClear(t *testing.T) {
	h, d := wakeHome(t)
	h.chatlog(d, "", "wake", "load", h.path("list.txt"))
	h.write("empty.txt", "# only a comment\n")
	if out, err := h.chatlog(d, "", "wake", "load", h.path("empty.txt")); err == nil || !strings.Contains(out, "--clear") {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, _ := h.chatlog(d, "", "wake"); !strings.Contains(out, "a.md") {
		t.Fatalf("the list was wiped by a refused load:\n%s", out)
	}
	if out, err := h.chatlog(d, "", "wake", "load", "--clear", h.path("empty.txt")); err != nil || !strings.Contains(out, "delete") {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, _ := h.chatlog(d, "", "wake"); !strings.Contains(out, "CHATLOG WAKE") {
		t.Fatalf("no list: the digest:\n%s", out)
	}
}

// Review (rv-compat): flags that ask for the digest and a dump of the list at once are an error, not a silent choice
func TestWakeDumpWithDigestFlagsIsRefused(t *testing.T) {
	h, d := wakeHome(t)
	h.chatlog(d, "", "wake", "load", h.path("list.txt"))
	for _, extra := range []string{"--kind", "--bytes", "--digest"} {
		args := []string{"wake", "--dump", h.path("p"), extra}
		if extra != "--digest" {
			args = append(args, "9000")
			if extra == "--kind" {
				args[len(args)-1] = "compact"
			}
		}
		if out, err := h.chatlog(d, "", args...); err == nil || !strings.Contains(out, "--dump") {
			t.Errorf("%v: %v\n%s", args, err, out)
		}
	}
	if _, err := os.Stat(h.path("p")); err == nil {
		t.Error("a refused dump wrote a dir")
	}
}

// Review (rv-compat): a wake/ key in a project's own notes is not read, and the run says so
func TestAProjectScopedWakeKeyIsSaidIgnored(t *testing.T) {
	h, d := wakeHome(t)
	h.chatlog(d, "", "wake", "load", h.path("list.txt"))
	st, err := store.Open(h.path("d/chatlog.db"))
	if err != nil {
		t.Fatal(err)
	}
	key := project.Resolve(h.dir, st).Key
	if _, err := st.KVWrite(context.Background(), key, map[string]string{"wake/file/09": "/etc/hostname"}, nil); err != nil {
		t.Fatal(err)
	}
	st.Close()
	out, err := h.chatlog(d, "", "wake")
	if err != nil || !strings.Contains(out, "wake/file/09") || !strings.Contains(out, "not read") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// Review (rv-delivery): the notes' warnings reach the operator
func TestWakeLoadPrintsTheNotesWarnings(t *testing.T) {
	h, d := wakeHome(t)
	h.write("long.txt", "step "+strings.Repeat("long ", 60)+"\n")
	if out, err := h.chatlog(d, "", "wake", "load", h.path("long.txt")); err != nil || !strings.Contains(out, "200") {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestWakeDumpWithNoDirIsAnError(t *testing.T) {
	h, d := wakeHome(t)
	h.chatlog(d, "", "wake", "load", h.path("list.txt"))
	if out, err := h.chatlog(d, "", "wake", "--dump", ""); err == nil || !strings.Contains(out, "--dump") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// Review (rv-tests-design, rv-correctness)
func TestWakeDumpWithoutAListIsRefusedAndLoadDryRunWritesNothing(t *testing.T) {
	h, d := wakeHome(t)
	if out, err := h.chatlog(d, "", "wake", "--dump", h.path("p")); err == nil || !strings.Contains(out, "no wake list") {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := h.chatlog(d, "", "wake", "load", "--dry-run", h.path("list.txt")); err != nil || !strings.Contains(out, "would write global/wake/file/01") {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, _ := h.chatlog(d, "", "wake"); !strings.Contains(out, "CHATLOG WAKE") {
		t.Fatalf("a dry run wrote a list:\n%s", out)
	}
}

func TestAGlobalDoneEndsTheWakeThroughTheCLIToo(t *testing.T) {
	h, d := wakeHome(t)
	h.chatlog(d, "", "wake", "load", h.path("list.txt"))
	in := fmt.Sprintf(`{"session_id":"s1","cwd":%q,"source":"compact"}`, h.dir)
	h.chatlog(d, in, "hook", "SessionStart")
	st, err := store.Open(h.path("d/chatlog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.KVWrite(context.Background(), "global", map[string]string{"sit/醒/done": "LOADED"}, nil); err != nil {
		t.Fatal(err)
	}
	if out, _ := h.chatlog(d, "", "wake"); strings.Contains(out, "kv_write sit/醒/done") && !strings.Contains(out, "sit/<g>/done") {
		t.Fatalf("the wake is done, the outline still names it owed:\n%s", out)
	}
}

// CI (macOS): the hook is given a cwd that goes through a link, the CLI runs in the directory itself: one project
func TestTheHookAndTheCLIAgreeOnADirectoryReachedThroughALink(t *testing.T) {
	h, d := wakeHome(t)
	real := h.path("real")
	os.MkdirAll(real, 0o700)
	link := h.path("lnk")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	h.chatlog(d, "", "wake", "load", h.path("list.txt"))
	hookIn := fmt.Sprintf(`{"session_id":"s1","cwd":%q,"source":"compact"}`, link)
	if out, err := h.chatlog(d, hookIn, "hook", "SessionStart"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	c := exec.Command(built, append(append([]string{}, d...), "wake")...)
	c.Env, c.Dir = h.env, real
	out, err := c.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `kv_write sit/醒/done = "<that LOADED line>"`) {
		t.Fatalf("the CLI in the real dir does not see what the hook armed through the link: %v\n%s", err, out)
	}
}
