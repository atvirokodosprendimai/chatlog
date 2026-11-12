// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package wake

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/atvirokodosprendimai/chatlog/internal/know"
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
	// PageMax is the most bytes of one page of a dump: a page is read by a session in one call.
	PageMax  = 24000
	pageGlob = "page-*.txt"
)

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

// Expand makes a listed path: a leading ~ and $HOME or ${HOME} are home, $PWD or ${PWD} the working directory. Nothing else
// is expanded (a note must not read the environment): a path with another $NAME stays as written and so is MISSING.
func Expand(p, home, pwd string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = home + p[1:]
	}
	return strings.NewReplacer("${HOME}", home, "$HOME", home, "${PWD}", pwd, "$PWD", pwd).Replace(p)
}

// File is one listed file as read.
type File struct {
	Path    string
	Lines   int
	Missing bool
	NotFile bool
	Cut     bool
	Content string
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

// Build reads the list: each file whole (up to FileMax; a path listed twice once), each prefix with full 1 for the project.
// A file that is not there is a line in the report, never an error.
func Build(ctx context.Context, st *store.Store, project string, l List, home, pwd string) (Report, error) {
	var r Report
	seen := map[string]bool{}
	for _, raw := range l.Files {
		p := filepath.Clean(Expand(raw, home, pwd))
		if seen[p] {
			continue
		}
		seen[p] = true
		r.Files = append(r.Files, readFile(p))
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

func readFile(p string) File {
	f := File{Path: p}
	fi, err := os.Stat(p)
	switch {
	case err != nil:
		f.Missing = true
		return f
	case !fi.Mode().IsRegular():
		f.NotFile = true
		return f
	}
	fh, err := os.Open(p)
	if err != nil {
		f.Missing = true
		return f
	}
	defer fh.Close()
	b, _ := io.ReadAll(io.LimitReader(fh, FileMax+1))
	if len(b) > FileMax {
		b = b[:FileMax]
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
	return f
}

// kvName is a prefix as the LOADED line names it: rule/ is rule.
func kvName(p string) string { return strings.TrimSuffix(p, "/") }

// Loaded is the line a session answers with, the counts of what the list holds: files read of listed, their lines, the
// notes under each prefix, and the files skipped by name.
func (r Report) Loaded() string {
	read, lines := 0, 0
	var skipped []string
	for _, f := range r.Files {
		if f.Missing || f.NotFile {
			skipped = append(skipped, f.Path)
			continue
		}
		read++
		lines += f.Lines
	}
	var kvs []string
	for _, p := range r.Prefixes {
		kvs = append(kvs, fmt.Sprintf("%s=%d", kvName(p.Prefix), len(p.Notes)))
	}
	s := "none"
	if len(skipped) > 0 {
		s = strings.Join(skipped, " ")
	}
	return fmt.Sprintf("LOADED files=%d/%d lines=%d/%d kv %s; skipped: %s", read, len(r.Files), lines, lines, strings.Join(kvs, " "), s)
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
		default:
			total += f.Lines
			fmt.Fprintf(&b, "  %5d  %s\n", f.Lines, f.Path)
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
	fmt.Fprintf(&b, "== ANSWER with (the counts you really read, if they differ): %s\n", r.Loaded())
	b.WriteString(Finish(groups...))
	b.WriteString("== FASTER: chatlog wake --dump DIR writes all of the above into pages of at most 24000 bytes: read every page in ONE turn, in parallel\n")
	return b.String()
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

// Dump writes content into dir as page-NN.txt, each at most PageMax bytes. The dir is made 0700 and must be a real
// directory of this user (a link or another user's is refused: the pages hold the user's notes); the pages of an earlier
// dump are removed first, other files are left alone.
func Dump(dir, content string) ([]Page, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
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
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	old, _ := filepath.Glob(filepath.Join(dir, pageGlob))
	for _, p := range old {
		if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
			if err := os.Remove(p); err != nil {
				return nil, err
			}
		}
	}
	var out []Page
	for i, pg := range Paginate(content, PageMax) {
		p := filepath.Join(dir, fmt.Sprintf("page-%02d.txt", i+1))
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return out, err
		}
		_, err = f.WriteString(pg)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return out, err
		}
		out = append(out, Page{p, len(pg)})
	}
	return out, nil
}
