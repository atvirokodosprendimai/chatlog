// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repo makes a git repository with one commit at dir and returns it.
func repo(t *testing.T, dir, file string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, file), []byte(file), 0o600)
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", file}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestASubdirectoryIsItsRepositorysProject(t *testing.T) {
	r := repo(t, filepath.Join(t.TempDir(), "webshop"), "a")
	os.MkdirAll(filepath.Join(r, "scripts", "deep"), 0o700)
	top, sub := Resolve(r, nil), Resolve(filepath.Join(r, "scripts", "deep"), nil)
	if !IsSha(top.Key) || top.Key != sub.Key || sub.Name != "webshop" || sub.Root != r {
		t.Fatalf("top %+v, subdirectory %+v", top, sub)
	}
}

func TestTwoRepositoriesWithTheSameFolderNameStayApart(t *testing.T) {
	a := repo(t, filepath.Join(t.TempDir(), "app"), "one")
	b := repo(t, filepath.Join(t.TempDir(), "app"), "two")
	ia, ib := Resolve(a, nil), Resolve(b, nil)
	if ia.Name != "app" || ib.Name != "app" || ia.Key == ib.Key {
		t.Fatalf("%+v %+v", ia, ib)
	}
}

func TestARenamedFolderIsTheSameProject(t *testing.T) {
	old := repo(t, filepath.Join(t.TempDir(), "webshop"), "a")
	before := Resolve(old, nil)
	renamed := filepath.Join(filepath.Dir(old), "shop")
	if err := os.Rename(old, renamed); err != nil {
		t.Fatal(err)
	}
	after := Resolve(renamed, nil)
	if after.Key != before.Key || after.Name != "shop" {
		t.Fatalf("before %+v, after %+v: the key must survive the rename and the name follow it", before, after)
	}
}

func TestWithoutARepositoryTheRootPathIsTheKey(t *testing.T) {
	d := filepath.Join(t.TempDir(), "notes")
	os.MkdirAll(d, 0o700)
	got := Resolve(d, nil)
	if got.Key != "path:"+d || got.Name != "notes" || got.Root != d {
		t.Fatalf("%+v", got)
	}
	if got := Resolve("/no/such/dir/at/all", nil); got.Key != "path:/no/such/dir/at/all" || got.Name != "all" {
		t.Fatalf("a directory that is gone is named by its path: %+v", got)
	}
	if got := Resolve("", nil); got.Key != "unknown" {
		t.Fatalf("%+v", got)
	}
}

func TestTheOverrideWinsAndIsLeftForTheStoreToResolve(t *testing.T) {
	t.Setenv("CHATLOG_PROJECT", " webshop-fleet ")
	r := repo(t, filepath.Join(t.TempDir(), "x"), "a")
	if got := Resolve(r, nil); got != (ID{Key: "webshop-fleet", Name: "webshop-fleet", Env: true}) {
		t.Fatalf("%+v", got)
	}
}

type memCache map[string]string

func (m memCache) RootKey(root string) (string, bool) { k, ok := m[root]; return k, ok }
func (m memCache) PutRootKey(root, key string)        { m[root] = key }

func TestTheCacheSavesWalkingTheHistory(t *testing.T) {
	r := repo(t, filepath.Join(t.TempDir(), "big"), "a")
	c := memCache{}
	k := Resolve(r, c).Key
	if c[r] != k {
		t.Fatalf("not cached: %v", c)
	}
	memo.Delete(r) // as in a new process
	c[r] = strings.Repeat("a", 40)
	if got := Resolve(r, c).Key; got != c[r] {
		t.Fatalf("the cache is asked first: %s", got)
	}
	memo.Delete(r)
}

func TestShort(t *testing.T) {
	sha := strings.Repeat("526abcd", 6)[:40]
	for in, want := range map[string]string{sha: "[526abcd]", "path:/u/x": "path:/u/x", "webshop": "webshop"} {
		if got := Short(in); got != want {
			t.Errorf("Short(%q) = %q, want %q", in, got, want)
		}
	}
}
