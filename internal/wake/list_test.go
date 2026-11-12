// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package wake

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/atvirokodosprendimai/chatlog/internal/know"
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
	if e, err := ReadList(context.Background(), listStore(t)); err != nil || !e.Empty() {
		t.Fatal("a store with no list has an empty one", err)
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
	if got := r.Loaded(); !strings.Contains(got, "files=3/5") || !strings.Contains(got, "lines=5") || !strings.Contains(got, "skipped: "+filepath.Join(dir, "gone.md")+" "+dir+";") && !strings.HasSuffix(got, "skipped: "+filepath.Join(dir, "gone.md")+" "+dir) {
		t.Errorf("loaded: %s", got)
	}
}

func TestAFileOverTheCapIsCutWithANote(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.txt")
	os.WriteFile(p, []byte(strings.Repeat("x\n", FileMax/2+100)), 0o600)
	r := buildOf(t, listStore(t), List{Files: []string{p}}, dir)
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
		"exact page":  strings.Repeat("x", PageMax),
		"rune cut":    "x" + strings.Repeat("醒", 40000), // the first cut falls inside a rune and backs off
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

func TestListFileLinesAndTheKeysTheyBecome(t *testing.T) {
	l, notes, err := ParseListFile(strings.NewReader("# c\n\nfile ~/a.md\nfile  $PWD/CLAUDE.md \nprefix rule/\nstep call skill x y\nnote sit/醒 wake | load\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(l.Files, "|") != "~/a.md|$PWD/CLAUDE.md" || l.Prefixes[0] != "rule/" || l.Steps[0] != "call skill x y" || len(notes) != 1 || notes[0].Key != "sit/醒" || notes[0].Value != "wake | load" {
		t.Fatalf("%+v %+v", l, notes)
	}
	k := l.Keys()
	if k["global/wake/file/01"] != "~/a.md" || k["global/wake/file/02"] != "$PWD/CLAUDE.md" || k["global/wake/prefix/01"] != "rule/" || k["global/wake/step/01"] != "call skill x y" || len(k) != 4 {
		t.Fatalf("%v", k)
	}
	for in, want := range map[string]string{"step ok\nfile\n": "line 2", "nope x\n": "line 1", "note onlykey\n": "line 1", "prefix \n": "line 1"} {
		if _, _, err := ParseListFile(strings.NewReader(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", in, err, want)
		}
	}
}

// Security review: the list is notes, and notes can be written by an agent that took a prompt injection. A listed file is
// read only when it is under the home or the project, and not a secret by name.
func TestAListedFileOutsideHomeAndProjectOrSecretByNameIsRefused(t *testing.T) {
	root := t.TempDir()
	home, work, other := filepath.Join(root, "h"), filepath.Join(root, "w"), filepath.Join(root, "x")
	for _, d := range []string{filepath.Join(home, ".ssh"), filepath.Join(home, "notes"), work, other} {
		os.MkdirAll(d, 0o700)
	}
	key, env := filepath.Join(home, ".ssh", "id_rsa"), filepath.Join(work, ".env")
	good, out := filepath.Join(home, "notes", "a.md"), filepath.Join(other, "o.md")
	for p, s := range map[string]string{key: "SECRET KEY", env: "TOKEN=1", good: "fine\n", out: "outside\n"} {
		os.WriteFile(p, []byte(s), 0o600)
	}
	os.Symlink(key, filepath.Join(work, "NOTES.md")) // a hostile repo's link to the key
	l := List{Files: []string{good, key, env, out, "/etc/hosts"}}
	r, err := Build(context.Background(), listStore(t), "webshop", l, home, work)
	if err != nil {
		t.Fatal(err)
	}
	c := r.Content()
	for _, secret := range []string{"SECRET KEY", "TOKEN=1", "outside"} {
		if strings.Contains(c, secret) {
			t.Errorf("%q was read:\n%s", secret, c)
		}
	}
	if !strings.Contains(c, "fine") || strings.Count(c, "=== REFUSED") != 4 {
		t.Errorf("one read, four refused:\n%s", c)
	}
	if got := r.Loaded(); !strings.Contains(got, "files=1/5") {
		t.Errorf("%s", got)
	}
	// each of these names the key by another road: a link inside the project, a .. through the home
	for _, via := range []string{"$PWD/NOTES.md", "~/notes/../.ssh/id_rsa"} {
		r, err := Build(context.Background(), listStore(t), "webshop", List{Files: []string{via}}, home, work)
		if err != nil || strings.Contains(r.Content(), "SECRET KEY") || !strings.Contains(r.Content(), "=== REFUSED") {
			t.Errorf("%s: %v\n%s", via, err, r.Content())
		}
	}
}

func TestTheFilesTogetherAreCappedAndAFifoIsNeverOpenedBlocking(t *testing.T) {
	dir := t.TempDir()
	var files []string
	for i := 0; i < 5; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f%d", i))
		os.WriteFile(p, []byte(strings.Repeat("y", FileMax)), 0o600)
		files = append(files, p)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	files = append(files, fifo)
	done := make(chan Report)
	go func() { done <- buildOf(t, listStore(t), List{Files: files}, dir) }()
	select {
	case r := <-done:
		c := r.Content()
		if strings.Count(c, "=== FILE") != 4 || !strings.Contains(c, "=== OVER BUDGET") || !strings.Contains(c, "=== NOT A FILE "+fifo) || len(c) > TotalMax+4000 {
			t.Fatalf("%d bytes, files %d", len(c), strings.Count(c, "=== FILE"))
		}
		if got := r.Loaded(); !strings.Contains(got, "files=4/6") {
			t.Errorf("%s", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a FIFO blocked the load")
	}
}

func TestOnlyTheFilesLimitsHoldForOtherNamesToo(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	os.WriteFile(p, []byte("x\n"), 0o600)
	os.Symlink(p, filepath.Join(dir, "b.md"))
	r := buildOf(t, listStore(t), List{Files: []string{p, filepath.Join(dir, "b.md"), filepath.Join(dir, "sub", "..", "a.md")}}, dir)
	if strings.Count(r.Content(), "=== FILE") != 1 {
		t.Errorf("one file under three names is read once:\n%s", r.Content())
	}
}

func TestADumpDirWithGlobCharactersLooseModeAndManyPages(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "a[b*c")
	if _, err := Dump(dir, "old one\n"+strings.Repeat("x", PageMax)); err != nil {
		t.Fatal(err)
	}
	if pages, err := Dump(dir, "new"); err != nil || len(pages) != 1 {
		t.Fatalf("%v %v", pages, err)
	}
	if es, _ := os.ReadDir(dir); len(es) != 2 || es[0].Name() != ".lock" || es[1].Name() != "page-01.txt" {
		t.Errorf("an old page stays when the dir name has glob characters: %v", es)
	}
	loose := filepath.Join(base, "loose")
	os.Mkdir(loose, 0o755)
	os.Chmod(loose, 0o755)
	if _, err := Dump(loose, "x"); err == nil || !strings.Contains(err.Error(), "700") {
		t.Errorf("a dir open to others is refused, saying what to do: %v", err)
	}
	many := filepath.Join(base, "many")
	pages, err := Dump(many, strings.Repeat("z", PageMax*100+1))
	if err != nil || len(pages) != 101 {
		t.Fatalf("%d pages, %v", len(pages), err)
	}
	if filepath.Base(pages[0].Path) != "page-001.txt" || filepath.Base(pages[100].Path) != "page-101.txt" {
		t.Errorf("past 99 pages the names still sort as the pages read: %s %s", pages[0].Path, pages[100].Path)
	}
}

func TestTwoDumpsAtOnceIntoOneDirLeaveACompleteSet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	content := strings.Repeat("0123456789\n", 6000)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Dump(dir, content); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a concurrent dump failed: %v", err)
	}
	var all strings.Builder
	pages, _ := filepath.Glob(filepath.Join(dir, "page-*.txt"))
	sort.Strings(pages)
	for _, p := range pages {
		b, _ := os.ReadFile(p)
		all.Write(b)
	}
	if all.String() != content {
		t.Errorf("the pages left are not the content: %d pages, %d bytes", len(pages), all.Len())
	}
}

func TestACutFileAndASkippedOneAreInTheLoadedLine(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.txt")
	os.WriteFile(p, []byte(strings.Repeat("x\n", FileMax)), 0o600)
	if got := buildOf(t, listStore(t), List{Files: []string{p}}, dir).Loaded(); !strings.Contains(got, "cut=1") {
		t.Errorf("%s", got)
	}
}

func TestANoteLineWritesOnlyUnderSit(t *testing.T) {
	for _, ok := range []string{"note sit/醒 x\n", "note global/sit/醒/on/1 startup\n"} {
		if _, _, err := ParseListFile(strings.NewReader(ok)); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"note hook/prompt/1 x\n", "note global/wake/file/01 /etc/passwd\n", "note wake/file/01 x\n", "note rule/x y\n"} {
		if _, _, err := ParseListFile(strings.NewReader(bad)); err == nil || !strings.Contains(err.Error(), "line 1") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// Review (rv-delivery): the delete set is what the load saw; a key changed meanwhile refuses the whole write.
func TestReplacePlanRefusesWhenAStaleKeyChangedMeanwhile(t *testing.T) {
	st := listStore(t)
	put(t, st, "global", map[string]string{"wake/step/01": "old one", "wake/step/02": "old two"}, nil)
	plan, err := PlanReplace(context.Background(), st, List{Steps: []string{"new"}}.Keys())
	if err != nil || len(plan.Del) != 1 || plan.Del[0] != "global/wake/step/02" {
		t.Fatalf("%+v %v", plan, err)
	}
	put(t, st, "global", map[string]string{"wake/step/02": "someone just wrote this"}, nil)
	if _, err := st.KVWritePlan(context.Background(), plan); err == nil {
		t.Fatal("the key changed after the load looked at it: the write must be refused")
	}
	plan, _ = PlanReplace(context.Background(), st, List{Steps: []string{"new"}}.Keys())
	if _, err := st.KVWritePlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if l, _ := ReadList(context.Background(), st); strings.Join(l.Steps, "|") != "new" {
		t.Fatalf("%v", l)
	}
}

// Review (rv-delivery): a read that fails is not a read
func TestAFileThatCannotBeReadIsNotCountedAsRead(t *testing.T) {
	f := File{Path: "/x"}
	readBody(&f, errReader{}, FileMax)
	if f.Err == "" || !strings.Contains(f.Err, "boom") {
		t.Fatalf("%+v", f)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, fmt.Errorf("boom") }

// Review (rv-correctness): a variable is its whole name; no home means no ~
func TestExpandTakesWholeNamesAndNeedsAHomeForTilde(t *testing.T) {
	for in, want := range map[string]string{"$HOMEDIR/x": "$HOMEDIR/x", "$PWDX/y": "$PWDX/y", "${HOME}x": "/hx", "$HOME-x": "/h-x", "$HOME": "/h"} {
		if got := Expand(in, "/h", "/w"); got != want {
			t.Errorf("Expand(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Expand("~/.bashrc", "", "/w"); got != "~/.bashrc" {
		t.Errorf("with no home a ~ stays, and so is MISSING: %q", got)
	}
}

// Review (rv-correctness): a file that exists but cannot be opened is not MISSING
func TestAFileThatExistsButIsNotReadableSaysSo(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads a mode 000 file")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "locked.md")
	os.WriteFile(p, []byte("x"), 0o000)
	r := buildOf(t, listStore(t), List{Files: []string{p}}, dir)
	if c := r.Content(); !strings.Contains(c, "=== READ ERROR "+p) || strings.Contains(c, "MISSING") {
		t.Fatalf("%s", c)
	}
}

// Review (rv-tests-design): a cut lands on a line end, not on the byte limit, and the cut part is exactly what fits
func TestACutFileEndsAtTheLastWholeLineThatFits(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lines.txt")
	line := "abcd\n" // 5 bytes: FileMax is not a multiple, the limit falls inside a line
	os.WriteFile(p, []byte(strings.Repeat(line, FileMax/5+50)), 0o600)
	r := buildOf(t, listStore(t), List{Files: []string{p}}, dir)
	f := r.Files[0]
	if !f.Cut || len(f.Content) != FileMax/5*5 || !strings.HasSuffix(f.Content, "\n") || f.Lines != FileMax/5 {
		t.Fatalf("cut %v, %d bytes, %d lines", f.Cut, len(f.Content), f.Lines)
	}
}

// Review (rv-tests-design): the longest line the hook can print is still short: a key of 128 bytes, a 16 byte source, a long temp dir
func TestTheLongestReminderFits(t *testing.T) {
	g := strings.Repeat("g", 128-len("sit/")-len("/done"))
	m := Reminder(know.Arm{Group: g, Source: strings.Repeat("s", 40), Since: time.Now()}, "/var/folders/zz/abcdefghijklmnopqrstuvwxyz0123456789/T/chatlog-wake-501-ab12cd34")
	if len(m) > 400 {
		t.Fatalf("%d bytes: %s", len(m), m)
	}
}
