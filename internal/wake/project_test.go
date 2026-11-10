// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package wake

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/chunk"
	"github.com/atvirokodosprendimai/chatlog/internal/ingest"
	"github.com/atvirokodosprendimai/chatlog/internal/project"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

// repo makes a git repository with one commit at dir.
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

// tab ingests one session that ran in cwd and says what the user typed.
func tab(t *testing.T, st *store.Store, cwd, id, words string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), id+".jsonl")
	lines := []string{
		fmt.Sprintf(`{"type":"user","uuid":"u-%s","sessionId":%q,"cwd":%q,"timestamp":"2026-11-08T09:00:00Z","origin":{"kind":"human"},"message":{"content":%q}}`, id, id, cwd, words),
		fmt.Sprintf(`{"type":"assistant","uuid":"a-%s","sessionId":%q,"timestamp":"2026-11-08T09:00:05Z","message":{"content":[{"type":"text","text":"done"}]}}`, id, id),
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&ingest.Ingester{Store: st, Limit: chunk.DefaultLimit}).File(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// Two tabs, two repositories with the same folder name: each one's tail, wake digest and notes hold its own work and the
// global notes, nothing of the other's.
func TestTwoTabsEachSeeTheirOwnTailWakeAndNotes(t *testing.T) {
	ctx, st := context.Background(), openStore(t)
	a := repo(t, filepath.Join(t.TempDir(), "app"), "one")
	b := repo(t, filepath.Join(t.TempDir(), "app"), "two")
	os.MkdirAll(filepath.Join(a, "scripts"), 0o700)
	tab(t, st, filepath.Join(a, "scripts"), "sa", "fix the release script of alpha")
	tab(t, st, b, "sb", "rotate the keys of beta")
	ka, kb := project.Resolve(a, st).Key, project.Resolve(b, st).Key
	if ka == kb || !project.IsSha(ka) {
		t.Fatalf("two repositories, two keys: %s %s", ka, kb)
	}
	for _, c := range []struct{ key, tailHas, tailNot string }{{ka, "alpha", "beta"}, {kb, "beta", "alpha"}} {
		items, _, err := st.Tail(ctx, c.key, "", 0, 100000)
		if err != nil || len(items) != 1 || !strings.Contains(items[0].Text, c.tailHas) {
			t.Fatalf("tail of %s: %+v %v", c.key, items, err)
		}
	}
	st.KVWrite(ctx, ka, map[string]string{"now/job": "alpha job", "global/rule/all": "for every project"}, nil)
	st.KVWrite(ctx, kb, map[string]string{"now/job": "beta job"}, nil)
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		key        string
		has, hasnt []string
	}{
		{ka, []string{"alpha job", "for every project", "fix the release script of alpha"}, []string{"beta"}},
		{kb, []string{"beta job", "for every project", "rotate the keys of beta"}, []string{"alpha"}},
	} {
		d, err := Digest(ctx, st, Compact, c.key, now, 24000)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range c.has {
			if !strings.Contains(d, w) {
				t.Errorf("the digest of %s lacks %q:\n%s", c.key, w, d)
			}
		}
		for _, w := range c.hasnt {
			if strings.Contains(d, w) {
				t.Errorf("the digest of %s holds %q of the other tab:\n%s", c.key, w, d)
			}
		}
	}
	if got, _ := st.KVRead(ctx, kb, nil, []string{"now/"}, time.Time{}); len(got) != 1 || got[0].Value != "beta job" {
		t.Errorf("kv_read of beta: %+v", got)
	}
}

// A renamed folder is the same project: its key, its tail and its notes stay; the new name is one more alias.
func TestRenamingTheFolderKeepsTheProject(t *testing.T) {
	ctx, st := context.Background(), openStore(t)
	old := repo(t, filepath.Join(t.TempDir(), "webshop"), "a")
	tab(t, st, old, "s1", "before the rename")
	key := project.Resolve(old, st).Key
	st.KVWrite(ctx, key, map[string]string{"fact/db": "postgres"}, nil)
	renamed := filepath.Join(filepath.Dir(old), "shop")
	if err := os.Rename(old, renamed); err != nil {
		t.Fatal(err)
	}
	tab(t, st, renamed, "s2", "after the rename")
	if again := project.Resolve(renamed, st).Key; again != key {
		t.Fatalf("the key changed with the folder: %s -> %s", key, again)
	}
	items, _, err := st.Tail(ctx, key, "", 0, 100000)
	if err != nil || len(items) != 2 {
		t.Fatalf("tail after the rename: %+v %v", items, err)
	}
	for _, name := range []string{"webshop", "shop", key} {
		if got, err := st.KVRead(ctx, name, []string{"fact/db"}, nil, time.Time{}); err != nil || len(got) != 1 || got[0].Value != "postgres" {
			t.Errorf("kv_read as %q: %+v %v", name, got, err)
		}
		if items, _, _ := st.Tail(ctx, name, "", 0, 100000); len(items) != 2 {
			t.Errorf("tail as %q: %d", name, len(items))
		}
	}
	if d, _ := Digest(ctx, st, Compact, "shop", time.Now(), 24000); !strings.Contains(d, "postgres") || !strings.Contains(d, "before the rename") || !strings.Contains(d, "after the rename") {
		t.Errorf("the digest after the rename:\n%s", d)
	}
}
