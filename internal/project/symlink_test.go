// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package project

import (
	"os"
	"path/filepath"
	"testing"
)

// The hook is given the session's cwd as Claude Code spells it, the CLI and the MCP server read their own working
// directory, which the system spells with its links resolved (macOS: /var/folders is /private/var/folders). One directory
// is one project whichever way it is reached.
func TestADirectoryReachedThroughALinkIsTheSameProject(t *testing.T) {
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(real, "notes")
	os.MkdirAll(filepath.Join(notes, "deep"), 0o700)
	link := filepath.Join(real, "link")
	if err := os.Symlink(notes, link); err != nil {
		t.Fatal(err)
	}
	direct, via, deep := FromCwd(notes, nil), FromCwd(link, nil), FromCwd(filepath.Join(link, "deep"), nil)
	if direct.Key != "path:"+notes || via.Key != direct.Key || via.Root != notes || deep.Key != "path:"+filepath.Join(notes, "deep") {
		t.Fatalf("direct %+v via link %+v", direct, via)
	}
	if deep.Root != filepath.Join(notes, "deep") {
		t.Errorf("a subdirectory through the link is spelled resolved: %+v", deep)
	}
}

func TestARepositoryReachedThroughALinkIsTheSameProjectWithTheResolvedRoot(t *testing.T) {
	real, _ := filepath.EvalSymlinks(t.TempDir())
	r := repo(t, filepath.Join(real, "webshop"), "a")
	link := filepath.Join(real, "ws-link")
	os.Symlink(r, link)
	a, b := FromCwd(r, nil), FromCwd(link, nil)
	if !IsSha(a.Key) || a.Key != b.Key || b.Root != r {
		t.Fatalf("%+v %+v", a, b)
	}
}

func TestADirectoryThatIsNotThereKeepsItsPathAsWritten(t *testing.T) {
	if got := FromCwd("/no/such/../such/dir", nil); got.Key != "path:/no/such/dir" {
		t.Fatalf("%+v", got)
	}
}

// Review: a link leads out of or into a repository: the directory is keyed by where it leads, the one spelling both sides agree on
func TestALinkIsKeyedByWhereItLeads(t *testing.T) {
	real, _ := filepath.EvalSymlinks(t.TempDir())
	outer := repo(t, filepath.Join(real, "outer"), "a")
	plain := filepath.Join(real, "plain")
	os.MkdirAll(plain, 0o700)
	out := filepath.Join(outer, "scratch") // inside the repository, leads out of it
	os.Symlink(plain, out)
	if got := FromCwd(out, nil); got.Key != "path:"+plain {
		t.Errorf("a link out of a repository: %+v", got)
	}
	into := filepath.Join(real, "into") // outside any repository, leads into one
	os.Symlink(outer, into)
	if got := FromCwd(into, nil); !IsSha(got.Key) || got.Root != outer {
		t.Errorf("a link into a repository: %+v", got)
	}
}
