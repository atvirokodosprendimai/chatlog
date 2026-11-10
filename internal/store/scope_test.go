// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func valuesIn(t *testing.T, s *Store, project string, prefixes ...string) string {
	t.Helper()
	got, err := s.KVRead(context.Background(), project, nil, prefixes, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range got {
		out = append(out, e.Key+"="+e.Value+"@"+e.Project)
	}
	return strings.Join(out, " ")
}

func TestNotesBelongToTheirProjectAndGlobalOnesToAll(t *testing.T) {
	ctx, s := context.Background(), open(t)
	must := func(project string, set map[string]string, del ...string) {
		t.Helper()
		if _, err := s.KVWrite(ctx, project, set, del); err != nil {
			t.Fatal(err)
		}
	}
	must("alpha", map[string]string{"now/job": "alpha job", "rule/x": "alpha rule"})
	must("beta", map[string]string{"now/job": "beta job"})
	must("alpha", map[string]string{"global/rule/all": "for everyone"})
	for project, want := range map[string]string{
		"alpha":       "now/job=alpha job@alpha rule/all=for everyone@global rule/x=alpha rule@alpha",
		"beta":        "now/job=beta job@beta rule/all=for everyone@global",
		"gamma":       "rule/all=for everyone@global",
		GlobalProject: "rule/all=for everyone@global",
	} {
		if got := valuesIn(t, s, project, ""); got != want {
			t.Errorf("project %s reads\n got %s\nwant %s", project, got, want)
		}
	}
	// the prefix global/ reads the global notes only, and "*" every project's
	if got := valuesIn(t, s, "alpha", GlobalPrefix); got != "rule/all=for everyone@global" {
		t.Errorf("global/ prefix: %s", got)
	}
	if got := valuesIn(t, s, AllProjects, "now/"); got != "now/job=alpha job@alpha now/job=beta job@beta" {
		t.Errorf("all projects: %s", got)
	}
	if got, _ := s.KVRead(ctx, "beta", []string{"global/rule/all", "now/job"}, nil, time.Time{}); len(got) != 2 {
		t.Errorf("named keys, one global and one the project's: %+v", got)
	}
}

func TestAProjectsOwnNoteWinsOverAGlobalOneAndItsDeletionMutesIt(t *testing.T) {
	ctx, s := context.Background(), open(t)
	s.KVWrite(ctx, "alpha", map[string]string{"global/rule/gate": "global gate"}, nil)
	if got := valuesIn(t, s, "alpha", "rule/"); got != "rule/gate=global gate@global" {
		t.Fatal(got)
	}
	s.KVWrite(ctx, "alpha", map[string]string{"rule/gate": "alpha gate"}, nil)
	if got := valuesIn(t, s, "alpha", "rule/"); got != "rule/gate=alpha gate@alpha" {
		t.Fatalf("own wins: %s", got)
	}
	if got := valuesIn(t, s, "beta", "rule/"); got != "rule/gate=global gate@global" {
		t.Fatalf("beta still reads the global one: %s", got)
	}
	s.KVWrite(ctx, "alpha", nil, []string{"rule/gate"})
	if got := valuesIn(t, s, "alpha", "rule/"); got != "" {
		t.Fatalf("a deletion in the project mutes the global note there: %s", got)
	}
	if got := valuesIn(t, s, "beta", "rule/"); got != "rule/gate=global gate@global" {
		t.Fatalf("and only there: %s", got)
	}
	if got := valuesIn(t, s, "alpha", "global/rule/"); got != "rule/gate=global gate@global" {
		t.Fatalf("the global note is still there to read by name: %s", got)
	}
	s.KVWrite(ctx, "alpha", nil, []string{"global/rule/gate"})
	if got := valuesIn(t, s, "beta", "rule/"); got != "" {
		t.Fatalf("deleting under global/ deletes it for everyone: %s", got)
	}
}

func TestAWriteNamesOneProjectAndAReadNamesOne(t *testing.T) {
	ctx, s := context.Background(), open(t)
	for _, p := range []string{AllProjects, "", "  "} {
		if _, err := s.KVWrite(ctx, p, map[string]string{"a": "b"}, nil); !errors.Is(err, ErrRefused) {
			t.Errorf("write for %q: %v", p, err)
		}
		if p != AllProjects {
			if _, err := s.KVRead(ctx, p, nil, []string{""}, time.Time{}); !errors.Is(err, ErrRefused) {
				t.Errorf("read for %q: %v", p, err)
			}
		}
	}
	if _, err := s.KVWrite(ctx, "alpha", map[string]string{"global/": "x"}, nil); !errors.Is(err, ErrRefused) {
		t.Errorf("an empty key under global/: %v", err)
	}
}

func TestLinksFollowTheScopeOfTheirSubject(t *testing.T) {
	ctx, s := context.Background(), open(t)
	s.KVWrite(ctx, "alpha", map[string]string{"rule/a": "alpha", "global/rule/g": "global"}, nil)
	if err := s.KVLink(ctx, "alpha", []Link{{S: "rule/a", P: "applies_to", O: "sit/x"}, {S: "global/rule/g", P: "applies_to", O: "sit/x"}}, nil); err != nil {
		t.Fatal(err)
	}
	for project, want := range map[string]string{"alpha": "rule/a rule/g", "beta": "rule/g"} {
		got, err := s.KVLinkedTo(ctx, project, "sit/x", time.Time{})
		if err != nil || strings.Join(got, " ") != want {
			t.Errorf("project %s: linked %v %v, want %s", project, got, err, want)
		}
	}
	if err := s.KVLink(ctx, "beta", []Link{{S: "rule/a", P: "applies_to", O: "sit/y"}}, nil); !errors.Is(err, ErrRefused) {
		t.Errorf("beta cannot link a note that is alpha's: %v", err)
	}
	if err := s.KVLink(ctx, "beta", []Link{{S: "rule/g", P: "applies_to", O: "sit/y"}}, nil); err != nil {
		t.Errorf("but it can link a global one: %v", err)
	}
	// a project's deletion of a global link, written without global/, hides it there only
	if err := s.KVLink(ctx, "alpha", nil, []Link{{S: "rule/g", P: "applies_to", O: "sit/x"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.KVLinkedTo(ctx, "beta", "sit/x", time.Time{}); strings.Join(got, " ") != "rule/g" {
		t.Errorf("beta keeps the global link: %v", got)
	}
	if got, _ := s.KVLinkedTo(ctx, "alpha", "sit/x", time.Time{}); strings.Join(got, " ") != "rule/a" {
		t.Errorf("alpha's own deletion of the global link: %v", got)
	}
	// written under global/ it is gone for everyone
	if err := s.KVLink(ctx, "alpha", nil, []Link{{S: "global/rule/g", P: "applies_to", O: "sit/x"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.KVLinkedTo(ctx, "beta", "sit/x", time.Time{}); len(got) != 0 {
		t.Errorf("deleted globally: %v", got)
	}
}

func TestNotesFoundByMeaningAreOnlyThoseOfTheProject(t *testing.T) {
	ctx, s := context.Background(), open(t)
	s.KVWrite(ctx, "alpha", map[string]string{"fact/a": "alpha fact", "global/fact/g": "global fact"}, nil)
	s.KVWrite(ctx, "beta", map[string]string{"fact/b": "beta fact"}, nil)
	pend, _ := s.KVPending(ctx, 10)
	if len(pend) != 3 {
		t.Fatalf("every project's notes get a vector: %+v", pend)
	}
	var rids []int64
	for _, p := range pend {
		rids = append(rids, p.ID)
	}
	keys := func(project, prefix string) []string {
		t.Helper()
		got, err := s.KVLatest(ctx, project, rids, prefix)
		if err != nil {
			t.Fatal(err)
		}
		var k []string
		for _, e := range got {
			k = append(k, e.Key)
		}
		slices.Sort(k)
		return k
	}
	for _, c := range []struct {
		project, prefix string
		want            []string
	}{
		{"alpha", "", []string{"fact/a", "fact/g"}}, {"beta", "", []string{"fact/b", "fact/g"}}, {"gamma", "", []string{"fact/g"}},
		{AllProjects, "", []string{"fact/a", "fact/b", "fact/g"}}, {"alpha", GlobalPrefix, []string{"fact/g"}}, {"alpha", "fact/a", []string{"fact/a"}},
	} {
		if got := keys(c.project, c.prefix); !slices.Equal(got, c.want) {
			t.Errorf("%s %q: %v, want %v", c.project, c.prefix, got, c.want)
		}
	}
	// a rewritten note keeps one vector per row, and only its last row is live in a project
	if err := s.KVMarkEmbedded(ctx, pend); err != nil {
		t.Fatal(err)
	}
	s.KVWrite(ctx, "alpha", map[string]string{"fact/a": "alpha fact, rewritten"}, nil)
	pend2, _ := s.KVPending(ctx, 10)
	if len(pend2) != 1 || !strings.Contains(pend2[0].Text, "rewritten") {
		t.Errorf("pending after a rewrite: %+v", pend2)
	}
}

func TestKVHasLooksInExactlyOneScope(t *testing.T) {
	ctx, s := context.Background(), open(t)
	s.KVWrite(ctx, GlobalProject, map[string]string{"rule/seed": "starter"}, nil)
	for project, want := range map[string]bool{GlobalProject: true, "alpha": false} {
		got, err := s.KVHas(ctx, project, []string{"rule/seed"})
		if err != nil || got["rule/seed"] != want {
			t.Errorf("%s: %v %v", project, got, err)
		}
	}
	if got, _ := s.KVHas(ctx, "alpha", []string{"global/rule/seed"}); !got["global/rule/seed"] {
		t.Errorf("a key under global/ is looked up in the global scope: %v", got)
	}
}

func TestEveryReadAndWriteFormOfAProjectName(t *testing.T) {
	ctx, s := context.Background(), open(t)
	key := "526abcd" + strings.Repeat("0", 33)
	if err := s.AddAlias(ctx, key, "gateway"); err != nil {
		t.Fatal(err)
	}
	s.KVWrite(ctx, "gateway", map[string]string{"now/x": "v"}, nil)
	for _, name := range []string{"gateway", key, "[" + key[:7] + "]", key[:7], key[:12]} {
		if got := valuesIn(t, s, name, "now/"); !strings.HasPrefix(got, "now/x=v@") {
			t.Errorf("%q does not name the project: %q", name, got)
		}
	}
}

func TestSetAndDeleteOfOneGlobalKeyUnderTwoSpellingsIsRefused(t *testing.T) {
	_, s := context.Background(), open(t)
	if _, err := s.KVWrite(context.Background(), GlobalProject, map[string]string{"x": "v"}, []string{"global/x"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("%v", err)
	}
}

func TestKVLatestTakesMoreRidsThanOneStatementBinds(t *testing.T) {
	ctx, s := context.Background(), open(t)
	s.KVWrite(ctx, "alpha", map[string]string{"a": "b"}, nil)
	rids := make([]int64, 40000)
	for i := range rids {
		rids[i] = int64(i + 1)
	}
	got, err := s.KVLatest(ctx, "alpha", rids, "")
	if err != nil || len(got) != 1 {
		t.Fatalf("%d %v", len(got), err)
	}
}

// A hook gives the write lock one second: a file that needs no migration must open without asking for it.
func TestOpeningAnUpToDateFileDoesNotWaitForTheWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tx, err := s.w.BeginTx(context.Background(), nil) // the lock, as the server holds it while it writes
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	start := time.Now()
	h, err := OpenWith(path, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("open while another process writes: %v", err)
	}
	h.Close()
	if time.Since(start) > 250*time.Millisecond {
		t.Fatalf("waited %v", time.Since(start))
	}
}
