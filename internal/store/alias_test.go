// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	keyA = "526abcd0000000000000000000000000000000aa"
	keyB = "526abce0000000000000000000000000000000bb"
)

func putKeyed(t *testing.T, s *Store, key, name string, minute int, text string) {
	t.Helper()
	at := time.Date(2026, 11, 2, 10, minute, 0, 0, time.UTC)
	r := Row{UUID: key[:8] + text, Session: "s", Source: "f", Time: at, End: at, Project: name, PKey: key, Words: "w", Answer: "a", Sha: text, Text: text}
	if err := s.Apply(context.Background(), "f-"+r.UUID, []Row{r}, Cursor{}); err != nil {
		t.Fatal(err)
	}
}

func TestTheFoldersNameBecomesAnAliasWhenFirstSeenAndNeverTwiceForTwoKeys(t *testing.T) {
	ctx, s := context.Background(), open(t)
	putKeyed(t, s, keyA, "app", 1, "a1")
	putKeyed(t, s, keyB, "app", 2, "b1")  // another repository, the same folder name: the alias stays with the first
	putKeyed(t, s, keyA, "shop", 3, "a2") // the folder was renamed: the same key gets a second name
	putKeyed(t, s, keyB, "my project", 4, "b2")
	putKeyed(t, s, keyB, "dead", 5, "b3") // a name that is a string of hex digits could be taken for a short sha: no alias
	ps, err := s.Projects(ctx)
	if err != nil || len(ps) != 2 {
		t.Fatalf("%+v %v", ps, err)
	}
	if strings.Join(ps[0].Aliases, " ") != "app shop" || len(ps[1].Aliases) != 0 || ps[0].Exchanges != 2 || ps[1].Exchanges != 3 {
		t.Fatalf("a: %+v\nb: %+v", ps[0], ps[1])
	}
	if got, _ := s.ResolveProject(ctx, "app"); got != keyA {
		t.Fatalf("app -> %s", got)
	}
	if s.Label(ctx, keyA) != "app" || s.Label(ctx, keyB) != "[526abce]" {
		t.Fatalf("labels %q %q", s.Label(ctx, keyA), s.Label(ctx, keyB))
	}
}

func TestAliasCommandsKeepOneKeyPerName(t *testing.T) {
	ctx, s := context.Background(), open(t)
	putKeyed(t, s, keyA, "app", 1, "a1")
	putKeyed(t, s, keyB, "other", 2, "b1")
	if err := s.AddAlias(ctx, "app", "gateway-N"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAlias(ctx, "gateway-N", "gateway-N"); err != nil {
		t.Fatalf("the same key twice is not a clash: %v", err)
	}
	if err := s.AddAlias(ctx, keyB[:9], "gateway-N"); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "[526abcd]") {
		t.Fatalf("a name another key holds: %v", err)
	}
	for _, bad := range []string{"", "a b", "[x]", "a/b", "a:b", "*", "global", "path:x", "526abc"} {
		if err := s.AddAlias(ctx, "app", bad); !errors.Is(err, ErrRefused) {
			t.Errorf("alias %q: %v", bad, err)
		}
	}
	if err := s.AddAlias(ctx, "ffff", "x"); !errors.Is(err, ErrRefused) {
		t.Errorf("a short sha no project has: %v", err)
	}
	if err := s.AddAlias(ctx, "*", "x"); !errors.Is(err, ErrRefused) {
		t.Errorf("not a project: %v", err)
	}
	if _, err := s.ResolveProject(ctx, "526abc"); !errors.Is(err, ErrRefused) {
		t.Errorf("a short sha that starts two projects must be refused, not guessed: %v", err)
	}
	if got, err := s.ResolveProject(ctx, "526abcd"); err != nil || got != keyA {
		t.Errorf("long enough to tell: %s %v", got, err)
	}
	if err := s.DelAlias(ctx, "gateway-N"); err != nil {
		t.Fatal(err)
	}
	if err := s.DelAlias(ctx, "gateway-N"); !errors.Is(err, ErrRefused) {
		t.Fatalf("removing what is not there: %v", err)
	}
	if got, _ := s.ResolveProject(ctx, "app"); got != keyA {
		t.Fatal("removing one name leaves the others")
	}
}

func TestEveryNameOfAProjectReadsItsTailAndFindsItsExchanges(t *testing.T) {
	ctx, s := context.Background(), open(t)
	putKeyed(t, s, keyA, "app", 1, "a1")
	putKeyed(t, s, keyA, "shop", 2, "a2")
	putKeyed(t, s, keyB, "app", 3, "b1")
	s.AddAlias(ctx, "app", "gateway-N")
	// exchanges stored before keys: a name and no key; the alias that names the project joins them to it
	old := Row{UUID: "old", Session: "s", Source: "f", Time: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Project: "shop", Words: "w", Answer: "a", Sha: "o", Text: "old"}
	if err := s.Apply(ctx, "f-old", []Row{old}, Cursor{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app", "shop", "gateway-N", keyA, "[526abcd]", "526abcd"} {
		items, _, err := s.Tail(ctx, name, "", 0, 1000)
		if err != nil || texts(items) != "old,a1,a2" {
			t.Errorf("tail of %q: %q %v", name, texts(items), err)
		}
	}
	if items, _, _ := s.Tail(ctx, keyB, "", 0, 1000); texts(items) != "b1" {
		t.Errorf("the other repository with the same folder name: %q", texts(items))
	}
	key, names, found, err := s.FindProject(ctx, "gateway-N")
	if err != nil || !found || key != keyA || len(names) != 3 {
		t.Errorf("%s %v %v %v", key, names, found, err)
	}
	if _, _, found, _ := s.FindProject(ctx, "somewhere"); found {
		t.Error("a plain word is no project: a search reads it as part of a path")
	}
}

// Every project `project ls` lists is named by the short sha it prints, in any of the forms a person types it: whatever
// the rows that hold the key (notes only, exchanges only, an alias only), with or without brackets, in any case.
func TestTheShortShaTheListingShowsNamesTheProjectEverywhere(t *testing.T) {
	ctx, s := context.Background(), open(t)
	putKeyed(t, s, keyA, "app", 1, "a1")                         // exchanges and an alias
	const notesOnly = "095355b00000000000000000000000000000000c" // the case of the report: digits first
	const aliasOnly = "1234567000000000000000000000000000000000"
	if _, err := s.KVWrite(ctx, notesOnly, map[string]string{"fact/x": "v"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.w.ExecContext(ctx, `INSERT INTO project_alias(alias, key) VALUES('lonely', ?)`, aliasOnly); err != nil {
		t.Fatal(err)
	}
	ps, err := s.Projects(ctx)
	if err != nil || len(ps) != 3 {
		t.Fatalf("%+v %v", ps, err)
	}
	for _, p := range ps {
		short := strings.Trim(shortKey(p.Key), "[]")
		for _, form := range []string{short, "[" + short + "]", strings.ToUpper(short), " " + short + " "} {
			if got, err := s.ResolveProject(ctx, form); err != nil || got != p.Key {
				t.Errorf("%q -> %q %v, want %s", form, got, err, p.Key)
			}
			if err := s.AddAlias(ctx, form, "n"+short); err != nil {
				t.Errorf("alias %q: %v", form, err)
			}
		}
	}
}

func TestAnAliasSpelledInCapitalsLikeHexStillResolves(t *testing.T) {
	ctx, s := context.Background(), open(t)
	putKeyed(t, s, keyA, "app", 1, "a1")
	if err := s.AddAlias(ctx, "app", "CAFE"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ResolveProject(ctx, "CAFE"); err != nil || got != keyA {
		t.Fatalf("CAFE -> %q %v", got, err)
	}
}
