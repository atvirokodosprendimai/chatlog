// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package wake

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/atvirokodosprendimai/chatlog/internal/know"
	"github.com/atvirokodosprendimai/chatlog/internal/redact"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

// The wake list (docs/wake.md) lives in the global notes: wake/file/<n> a path, wake/prefix/<n> a kv prefix, wake/step/<n>
// a free line. Keys sort as text, so numbers are zero-padded.
const (
	ListPrefix = store.GlobalPrefix + "wake/"
	KindFile   = "file"
	KindPrefix = "prefix"
	KindStep   = "step"
	// FileMax is the most of one listed file that is read; a longer one is cut at a line end and says so.
	FileMax = 2 << 20
	// TotalMax is the most of all the listed files together: the notes can be written by an agent, so the load has a bound.
	TotalMax = 8 << 20
	// PageMax is the most bytes of one page of a dump: a page is read by a session in one call.
	PageMax = 24000
)

// secretDirs are path segments a listed file may not sit under, whatever the home says.
var secretDirs = map[string]bool{".ssh": true, ".gnupg": true, ".aws": true, ".kube": true, ".docker": true}

var pageName = regexp.MustCompile(`^page-\d+\.txt(\.tmp)?$`)

// List is the wake list as the notes hold it, each kind in key order.
type List struct {
	Files, Prefixes, Steps []string
}

// Empty says there is no list: `chatlog wake` then prints the digest, as it did before the list existed.
func (l List) Empty() bool { return len(l.Files)+len(l.Prefixes)+len(l.Steps) == 0 }

// ReadList reads the list from the global notes; a project's own wake/ keys are not part of it.
func ReadList(ctx context.Context, st *store.Store) (List, error) {
	kvs, err := st.KVRead(ctx, store.GlobalProject, nil, []string{ListPrefix}, time.Time{})
	if err != nil {
		return List{}, err
	}
	var l List
	for _, e := range kvs {
		rest := strings.TrimPrefix(strings.TrimPrefix(e.Key, store.GlobalPrefix), "wake/")
		kind, _, _ := strings.Cut(rest, "/")
		v := strings.TrimSpace(e.Value)
		if v == "" {
			continue
		}
		switch kind {
		case KindFile:
			l.Files = append(l.Files, v)
		case KindPrefix:
			l.Prefixes = append(l.Prefixes, v)
		case KindStep:
			l.Steps = append(l.Steps, v)
		}
	}
	return l, nil
}

// Expand makes a listed path: a leading ~ and the whole variables $HOME, ${HOME} are home, $PWD, ${PWD} the working
// directory. Nothing else is expanded (a note must not read the environment): another $NAME stays as written and so is
// MISSING; with no home a ~ and $HOME stay too.
func Expand(p, home, pwd string) string {
	if home != "" && (p == "~" || strings.HasPrefix(p, "~/")) {
		p = home + p[1:]
	}
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] != '$' {
			b.WriteByte(p[i])
			continue
		}
		name, n := varAt(p[i+1:])
		switch {
		case name == "HOME" && home != "":
			b.WriteString(home)
		case name == "PWD":
			b.WriteString(pwd)
		default:
			b.WriteByte('$')
			continue
		}
		i += n
	}
	return b.String()
}

// varAt reads a variable name at the start of s, NAME or {NAME}, and the bytes it takes; "" when there is none.
func varAt(s string) (string, int) {
	if strings.HasPrefix(s, "{") {
		if end := strings.IndexByte(s, '}'); end > 0 {
			return s[1:end], end + 1
		}
		return "", 0
	}
	n := 0
	for n < len(s) && (s[n] == '_' || s[n] >= '0' && s[n] <= '9' || s[n] >= 'a' && s[n] <= 'z' || s[n] >= 'A' && s[n] <= 'Z') {
		n++
	}
	return s[:n], n
}

// File is one listed file as read.
type File struct {
	Path    string
	Lines   int
	Missing bool
	NotFile bool
	Cut     bool
	Over    bool   // the files before it already took TotalMax
	Refused string // why a file that is there is not read: outside the home and the project, or a secret by name
	Err     string // a read that failed
	Content string
}

// skipped says the file is not part of what was loaded.
func (f File) skipped() bool {
	return f.Missing || f.NotFile || f.Over || f.Refused != "" || f.Err != ""
}

// Prefix is one listed prefix as read: its notes, full.
type Prefix struct {
	Prefix string
	Notes  []store.KV
}

// Report is everything a wake lists, read.
type Report struct {
	Files    []File
	Prefixes []Prefix
	Steps    []string
}

// Build reads the list: each file whole (a file at most FileMax, all of them at most TotalMax; a file listed twice, by
// whatever name, once), each prefix with full 1 for the project. A file is read only when it is under the home or the
// working directory and not a secret by name. A file that is not there is a line in the report, never an error.
func Build(ctx context.Context, st *store.Store, project string, l List, home, pwd string) (Report, error) {
	var r Report
	seen := map[string]bool{}
	used := 0
	roots := realRoots(home, pwd)
	for _, raw := range l.Files {
		p := filepath.Clean(Expand(raw, home, pwd))
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			if !seen[p] {
				seen[p] = true
				r.Files = append(r.Files, File{Path: p, Missing: true})
			}
			continue
		}
		if seen[real] {
			continue
		}
		seen[real] = true
		f := readFile(p, real, roots, TotalMax-used)
		used += len(f.Content)
		r.Files = append(r.Files, f)
	}
	for _, p := range l.Prefixes {
		kvs, err := st.KVReadFull(ctx, project, nil, []string{p}, time.Time{}, false, true)
		if err != nil {
			return r, fmt.Errorf("prefix %q: %w", p, err)
		}
		r.Prefixes = append(r.Prefixes, Prefix{p, kvs})
	}
	r.Steps = l.Steps
	return r, nil
}

func realRoots(dirs ...string) []string {
	var out []string
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if real, err := filepath.EvalSymlinks(d); err == nil {
			out = append(out, real)
		}
	}
	return out
}

// refusal says why a resolved path is not read, "" when it is.
func refusal(real string, roots []string) string {
	under := false
	for _, r := range roots {
		if rel, err := filepath.Rel(r, real); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			under = true
		}
	}
	if !under {
		return "not under the home or the working directory"
	}
	for _, seg := range strings.Split(real, string(filepath.Separator)) {
		if secretDirs[seg] {
			return "a secret by name (" + seg + ")"
		}
	}
	base := filepath.Base(real)
	if base == ".netrc" || len(redact.DotenvNames(base)) > 0 {
		return "a secret by name (" + base + ")"
	}
	return ""
}

func readFile(p, real string, roots []string, budget int) File {
	f := File{Path: p}
	if why := refusal(real, roots); why != "" {
		f.Refused = why
		return f
	}
	// opened without blocking and without following a link, and judged on the open descriptor: a FIFO swapped in after
	// the check cannot hold the load
	fh, err := os.OpenFile(real, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			f.Missing = true
		} else {
			f.Err = err.Error()
		}
		return f
	}
	defer fh.Close()
	if fi, err := fh.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.NotFile = true
		return f
	}
	if budget <= 0 {
		f.Over = true
		return f
	}
	readBody(&f, fh, min(FileMax, budget))
	return f
}

// readBody reads at most limit bytes of a file into f, cut at a line end when there is more, and counts its lines.
func readBody(f *File, r io.Reader, limit int) {
	b, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		f.Err = err.Error()
		return
	}
	if len(b) > limit {
		b = b[:limit]
		if i := strings.LastIndexByte(string(b), '\n'); i >= 0 {
			b = b[:i+1]
		}
		f.Cut = true
	}
	f.Content = string(b)
	f.Lines = strings.Count(f.Content, "\n")
	if f.Content != "" && !strings.HasSuffix(f.Content, "\n") {
		f.Lines++
	}
}

// kvName is a prefix as the LOADED line names it: rule/ is rule.
func kvName(p string) string { return strings.TrimSuffix(p, "/") }

// Loaded is the line a session answers with, the counts of what the list holds: files read of listed, their lines, the
// notes under each prefix, the files cut, and the files skipped by name.
func (r Report) Loaded() string {
	read, lines, cut := 0, 0, 0
	var skipped []string
	for _, f := range r.Files {
		if f.skipped() {
			skipped = append(skipped, f.Path)
			continue
		}
		read++
		lines += f.Lines
		if f.Cut {
			cut++
		}
	}
	var kvs []string
	for _, p := range r.Prefixes {
		kvs = append(kvs, fmt.Sprintf("%s=%d", kvName(p.Prefix), len(p.Notes)))
	}
	s := "none"
	if len(skipped) > 0 {
		s = strings.Join(skipped, " ")
	}
	c := ""
	if cut > 0 {
		c = fmt.Sprintf("; cut=%d", cut)
	}
	return fmt.Sprintf("LOADED files=%d/%d lines=%d kv %s%s; skipped: %s", read, len(r.Files), lines, strings.Join(kvs, " "), c, s)
}

// Outline is what the wake lists, for a session to read: the files with their line counts, the prefixes with their sizes,
// the steps, and the line to answer with. groups are the owed situations, whose done keys the last line names.
func (r Report) Outline(groups ...string) string {
	var b strings.Builder
	b.WriteString("== FILES: read each WHOLE (all pages)\n")
	total := 0
	for _, f := range r.Files {
		switch {
		case f.Missing:
			fmt.Fprintf(&b, "  MISSING %s\n", f.Path)
		case f.NotFile:
			fmt.Fprintf(&b, "  NOT A FILE %s\n", f.Path)
		case f.Refused != "":
			fmt.Fprintf(&b, "  REFUSED %s (%s)\n", f.Path, f.Refused)
		case f.Err != "":
			fmt.Fprintf(&b, "  READ ERROR %s (%s)\n", f.Path, f.Err)
		case f.Over:
			fmt.Fprintf(&b, "  OVER BUDGET %s (the files before it took %d bytes)\n", f.Path, TotalMax)
		default:
			total += f.Lines
			cut := ""
			if f.Cut {
				cut = fmt.Sprintf("  (CUT at %d bytes)", FileMax)
			}
			fmt.Fprintf(&b, "  %5d  %s%s\n", f.Lines, f.Path, cut)
		}
	}
	fmt.Fprintf(&b, "   total lines: %d\n== KV: kv_read each prefix with full 1 (whole)\n", total)
	for _, p := range r.Prefixes {
		fmt.Fprintf(&b, "  %s (%d notes)\n", p.Prefix, len(p.Notes))
	}
	if len(r.Steps) > 0 {
		b.WriteString("== STEPS:\n")
		for _, s := range r.Steps {
			fmt.Fprintf(&b, "  %s\n", s)
		}
	}
	b.WriteString(r.Answer(groups...))
	fmt.Fprintf(&b, "== FASTER: chatlog wake --dump DIR writes all of the above into pages of at most %d bytes: read every page in ONE turn, in parallel\n", PageMax)
	return b.String()
}

// Answer is the line to answer with and the key that ends the wake, for each owed situation.
func (r Report) Answer(groups ...string) string {
	return "== ANSWER with (the counts you really read, if they differ): " + r.Loaded() + "\n" + Finish(groups...)
}

// Finish is the line that ends a wake: the key to write with the LOADED line, for each owed situation (a generic one when
// the reader did not find any owed for this project).
func Finish(groups ...string) string {
	if len(groups) == 0 {
		return "== THEN: kv_write sit/<g>/done = \"<that LOADED line>\" for the situation the reminder names; that ends the reminder\n"
	}
	var b strings.Builder
	for _, g := range groups {
		fmt.Fprintf(&b, "== THEN: kv_write %s = \"<that LOADED line>\"; that ends the reminder\n", know.DoneKey(g))
	}
	return b.String()
}

// Content is everything the list holds, as the text a dump pages: a header line, then the text, per file, prefix and step.
func (r Report) Content() string {
	var b strings.Builder
	for _, f := range r.Files {
		switch {
		case f.Missing:
			fmt.Fprintf(&b, "=== MISSING %s\n", f.Path)
			continue
		case f.NotFile:
			fmt.Fprintf(&b, "=== NOT A FILE %s\n", f.Path)
			continue
		case f.Refused != "":
			fmt.Fprintf(&b, "=== REFUSED %s (%s)\n", f.Path, f.Refused)
			continue
		case f.Err != "":
			fmt.Fprintf(&b, "=== READ ERROR %s (%s)\n", f.Path, f.Err)
			continue
		case f.Over:
			fmt.Fprintf(&b, "=== OVER BUDGET %s (the files before it took %d bytes)\n", f.Path, TotalMax)
			continue
		}
		fmt.Fprintf(&b, "=== FILE %s (%d lines)\n%s", f.Path, f.Lines, f.Content)
		if f.Content != "" && !strings.HasSuffix(f.Content, "\n") {
			b.WriteString("\n")
		}
		if f.Cut {
			fmt.Fprintf(&b, "=== CUT at %d bytes: the file is longer, read the rest from the file\n", FileMax)
		}
	}
	for _, p := range r.Prefixes {
		fmt.Fprintf(&b, "=== KV %s (%d notes)\n", p.Prefix, len(p.Notes))
		for _, n := range p.Notes {
			fmt.Fprintf(&b, "%s: %s\n", n.Key, n.Value)
			if n.Body != "" {
				b.WriteString(n.Body)
				if !strings.HasSuffix(n.Body, "\n") {
					b.WriteString("\n")
				}
			}
		}
	}
	for _, s := range r.Steps {
		fmt.Fprintf(&b, "=== STEP %s\n", s)
	}
	return b.String()
}

// Paginate cuts s into pages of at most max bytes, at line ends; a line longer than a page is cut on a rune boundary.
// The pages joined are s.
func Paginate(s string, max int) []string {
	var pages []string
	for len(s) > 0 {
		if len(s) <= max {
			pages = append(pages, s)
			break
		}
		cut := strings.LastIndexByte(s[:max], '\n') + 1
		if cut == 0 {
			cut = max
			for cut > 0 && !utf8.RuneStart(s[cut]) {
				cut--
			}
			if cut == 0 {
				cut = max
			}
		}
		pages = append(pages, s[:cut])
		s = s[cut:]
	}
	return pages
}

// Page is one written page.
type Page struct {
	Path  string
	Bytes int
}

// Dump writes content into dir as page-NN.txt, each at most PageMax bytes (NN has as many digits as the page count needs,
// two at least, so the names sort as the pages read). A dir that is not there is made 0700; one that is must be a real
// directory of this user and closed to others (a link, another user's, or an open one is refused: the pages hold the
// user's notes). Writers are serialised by a lock in the dir; a page is written whole under a temporary name and renamed
// into place, and only then are the pages of an earlier dump that the new one does not replace removed. Other files in the dir
// are left alone.
func Dump(dir, content string) ([]Page, error) {
	created := false
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		created = true
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("dump dir %s is not a directory (a link is refused too)", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return nil, fmt.Errorf("dump dir %s belongs to another user: name another with --dump", dir)
	}
	if !created && fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("dump dir %s is open to other users: chmod 700 it, or name another with --dump", dir)
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	pages := Paginate(content, PageMax)
	width := max(2, len(strconv.Itoa(len(pages))))
	var out []Page
	keep := map[string]bool{}
	for i, pg := range pages {
		name := fmt.Sprintf("page-%0*d.txt", width, i+1)
		p := filepath.Join(dir, name)
		if err := writeAtomic(p, pg); err != nil {
			return out, err
		}
		keep[name] = true
		out = append(out, Page{p, len(pg)})
	}
	es, err := os.ReadDir(dir)
	if err != nil {
		return out, err
	}
	for _, e := range es {
		if pageName.MatchString(e.Name()) && !keep[e.Name()] && e.Type().IsRegular() {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return out, err
			}
		}
	}
	return out, nil
}

// writeAtomic writes p whole: a temporary file beside it, renamed over it, so a reader sees the old page or the new one.
func writeAtomic(p, s string) error {
	tmp := p + ".tmp"
	os.Remove(tmp) // ours: the dir lock is held
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(s)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}
