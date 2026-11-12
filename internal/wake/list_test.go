// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package wake

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

func listStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func put(t *testing.T, st *store.Store, project string, set map[string]string, body map[string]string) {
	t.Helper()
	if _, err := st.KVWritePlan(context.Background(), store.KVPlan{Project: project, Set: set, Body: body}); err != nil {
		t.Fatal(err)
	}
}

func TestExpandKnowsTildeHomeAndPwdAndNothingElse(t *testing.T) {
	for in, want := range map[string]string{
		"~/a.md": "/h/a.md", "~": "/h", "$HOME/a": "/h/a", "${HOME}/a": "/h/a", "$PWD/CLAUDE.md": "/w/CLAUDE.md", "${PWD}/x": "/w/x",
		"a~b": "a~b", "$SECRET/x": "$SECRET/x", "/abs/p": "/abs/p", "~other/x": "~other/x",
	} {
		if got := Expand(in, "/h", "/w"); got != want {
			t.Errorf("Expand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTheListIsTheGlobalNotesByKindAndKey(t *testing.T) {
	st := listStore(t)
	put(t, st, "global", map[string]string{
		"wake/file/02": "~/b.md", "wake/file/01": "~/a.md", "wake/prefix/1": "rule/", "wake/step/1": "call the skill x", "wake/other/1": "x",
	}, nil)
	put(t, st, "webshop", map[string]string{"wake/file/03": "/not/global"}, nil) // a project's own is not the shared list
	l, err := ReadList(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(l.Files, "|") != "~/a.md|~/b.md" || strings.Join(l.Prefixes, "|") != "rule/" || strings.Join(l.Steps, "|") != "call the skill x" {
		t.Fatalf("%+v", l)
	}
	if l.Empty() {
		t.Fatal("not empty")
	}
	if e, _ := ReadList(context.Background(), listStore(t)); !e.Empty() {
		t.Fatal("a store with no list has an empty one")
	}
}

func buildOf(t *testing.T, st *store.Store, l List, pwd string) Report {
	t.Helper()
	r, err := Build(context.Background(), st, "webshop", l, "/nohome", pwd)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBuildCountsLinesAndSaysMissingNeverFails(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.md"), filepath.Join(dir, "b.md")
	os.WriteFile(a, []byte("one\ntwo\nthree\n"), 0o600)
	os.WriteFile(b, []byte("no newline at the end"), 0o600)
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("project\n"), 0o600)
	l := List{Files: []string{a, filepath.Join(dir, "gone.md"), dir, b, a, "$PWD/CLAUDE.md"}}
	r := buildOf(t, listStore(t), l, dir)
	out := r.Outline()
	for _, w := range []string{"     3  " + a, "     1  " + b, "     1  " + filepath.Join(dir, "CLAUDE.md"), "MISSING " + filepath.Join(dir, "gone.md"), "NOT A FILE " + dir, "total lines: 5"} {
		if !strings.Contains(out, w) {
			t.Errorf("%q missing from\n%s", w, out)
		}
	}
	if strings.Count(out, a) != 1 {
		t.Errorf("a path listed twice is read once:\n%s", out)
	}
	if got := r.Loaded(); !strings.Contains(got, "files=3/5") || !strings.Contains(got, "lines=5/5") || !strings.Contains(got, "skipped: ") {
		t.Errorf("loaded: %s", got)
	}
}

func TestAFileOverTheCapIsCutWithANote(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big.txt")
	os.WriteFile(p, []byte(strings.Repeat("x\n", FileMax/2+100)), 0o600)
	r := buildOf(t, listStore(t), List{Files: []string{p}}, "/w")
	c := r.Content()
	if !strings.Contains(c, "CUT at") || len(c) > FileMax+400 {
		t.Fatalf("cut: %d bytes", len(c))
	}
}

func TestBuildReadsEachPrefixWholeWithBodiesAndCountsThem(t *testing.T) {
	st := listStore(t)
	put(t, st, "webshop", map[string]string{"rule/a": "head a", "rule/b": "head b", "known/k": "lesson"}, map[string]string{"rule/a": "the long body of a"})
	r := buildOf(t, st, List{Prefixes: []string{"rule/", "known/", "lesson/"}, Steps: []string{"call skill x"}}, "/w")
	c := r.Content()
	for _, w := range []string{"rule/a: head a", "the long body of a", "rule/b: head b", "known/k: lesson", "call skill x", "=== KV lesson/ (0 notes)"} {
		if !strings.Contains(c, w) {
			t.Errorf("%q missing from\n%s", w, c)
		}
	}
	if got := r.Loaded(); !strings.Contains(got, "rule=2 known=1 lesson=0") {
		t.Errorf("loaded: %s", got)
	}
	out := r.Outline()
	if !strings.Contains(out, "LOADED files=0/0") || !strings.Contains(out, "rule/") || !strings.Contains(out, "call skill x") {
		t.Errorf("outline:\n%s", out)
	}
}

func TestOutlineNamesTheKeyThatFinishesTheWake(t *testing.T) {
	r := buildOf(t, listStore(t), List{}, "/w")
	if out := r.Outline("醒"); !strings.Contains(out, `kv_write sit/醒/done = "<that LOADED line>"`) {
		t.Errorf("%s", out)
	}
	if out := r.Outline(); !strings.Contains(out, "sit/<g>/done") || !strings.Contains(out, "chatlog wake --dump") {
		t.Errorf("%s", out)
	}
}

func TestPagesAreWithinTheCapAndJoinToTheContent(t *testing.T) {
	long := strings.Repeat("é", 30000) // one line over the cap, two-byte runes
	for name, s := range map[string]string{
		"lines":       strings.Repeat("a line of text\n", 5000),
		"no newline":  strings.Repeat("abc", 20000),
		"long line":   "head\n" + long + "\ntail\n",
		"exact":       strings.Repeat("x", PageMax-1) + "\n" + "y\n",
		"one big nl":  strings.Repeat("\n", PageMax*2+3),
		"small":       "tiny",
		"three-bytes": strings.Repeat("醒", 40000),
	} {
		pages := Paginate(s, PageMax)
		if strings.Join(pages, "") != s {
			t.Errorf("%s: the pages joined are not the content", name)
		}
		for i, p := range pages {
			if len(p) > PageMax || len(p) == 0 || !utf8.ValidString(p) {
				t.Errorf("%s: page %d is %d bytes, valid %v", name, i, len(p), utf8.ValidString(p))
			}
			if i < len(pages)-1 && name == "lines" && !strings.HasSuffix(p, "\n") {
				t.Errorf("%s: page %d does not end at a line end", name, i)
			}
		}
	}
	if len(Paginate("", PageMax)) != 0 {
		t.Error("no content, no pages")
	}
}

func TestDumpWritesPagesReplacesOnlyItsOwnAndRefusesALink(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "w")
	content := strings.Repeat("0123456789\n", 6000)
	pages, err := Dump(dir, content)
	if err != nil || len(pages) != 3 {
		t.Fatalf("%v %v", pages, err)
	}
	var back strings.Builder
	for _, p := range pages {
		b, err := os.ReadFile(p.Path)
		if err != nil || len(b) != p.Bytes || len(b) > PageMax {
			t.Fatalf("%+v %v", p, err)
		}
		if fi, _ := os.Stat(p.Path); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", fi.Mode())
		}
		back.Write(b)
	}
	if back.String() != content {
		t.Fatal("the pages on disk are not the content")
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", fi.Mode())
	}
	os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("mine"), 0o600)
	if pages, err = Dump(dir, "short"); err != nil || len(pages) != 1 {
		t.Fatalf("%v %v", pages, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "page-02.txt")); err == nil {
		t.Error("an old page stays and would be read as part of the new wake")
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.txt")); err != nil {
		t.Error("a file that is not a page was removed")
	}
	link := filepath.Join(t.TempDir(), "l")
	os.Symlink(dir, link)
	if _, err := Dump(link, "x"); err == nil {
		t.Error("a dump dir that is a link is refused")
	}
	file := filepath.Join(t.TempDir(), "f")
	os.WriteFile(file, nil, 0o600)
	if _, err := Dump(file, "x"); err == nil {
		t.Error("a dump dir that is a file is refused")
	}
}
