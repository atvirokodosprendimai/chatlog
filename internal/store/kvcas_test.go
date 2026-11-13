package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestKVIfAbsentClaimsAKeyOnceAndTheRefusalNamesTheHolder(t *testing.T) {
	ctx, s := context.Background(), open(t)
	if _, err := s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"task/adr/7": "claimed by a"}, IfAbsent: []string{"task/adr/7"}}); err != nil {
		t.Fatal(err)
	}
	_, err := s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"task/adr/7": "claimed by b", "other": "x"}, IfAbsent: []string{"task/adr/7"}})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("a live key: %v", err)
	}
	for _, want := range []string{"task/adr/7", "claimed by a", "in project p"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal names %q: %v", want, err)
		}
	}
	got, _ := s.KVRead(ctx, "p", nil, []string{""}, time.Time{})
	if len(got) != 1 || got[0].Value != "claimed by a" {
		t.Fatalf("a refusal writes nothing, also not the keys that passed: %v", got)
	}
	if _, err := s.KVWritePlan(ctx, KVPlan{Project: "p", Del: []string{"task/adr/7"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"task/adr/7": "claimed by b"}, IfAbsent: []string{"task/adr/7"}}); err != nil {
		t.Fatalf("a deleted key is absent: %v", err)
	}
}

func TestKVIfValueIsACompareAndSwapAndNamesEveryFailingKey(t *testing.T) {
	ctx, s := context.Background(), open(t)
	if _, err := s.KVWrite(ctx, "p", map[string]string{"n/next": "7", "n/other": "a"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"n/next": "8"}, IfValue: map[string]string{"n/next": "7"}}); err != nil {
		t.Fatal(err)
	}
	_, err := s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"n/next": "9", "n/new": "z"},
		IfValue: map[string]string{"n/next": "7", "n/other": "b", "n/missing": "q"}})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("%v", err)
	}
	for _, want := range []string{`"n/next"`, `"8"`, `"n/other"`, `"a"`, `"n/missing"`, "never written"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("every failing key with its current value, %q: %v", want, err)
		}
	}
	if got, _ := s.KVRead(ctx, "p", []string{"n/next", "n/new"}, nil, time.Time{}); len(got) != 1 || got[0].Value != "8" {
		t.Fatalf("nothing written: %v", got)
	}
	if _, err := s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"a": "b"}, IfValue: map[string]string{"n/next": ""}}); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "if_absent") {
		t.Fatalf("an empty expected value is refused with the advice to use if_absent: %v", err)
	}
}

func TestKVOfParallelClaimsOfOneKeyExactlyOneWins(t *testing.T) {
	ctx, s := context.Background(), open(t)
	var won, refused atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"task/adr/9": "mine"}, IfAbsent: []string{"task/adr/9"}}); err == nil {
				won.Add(1)
			} else if errors.Is(err, ErrRefused) {
				refused.Add(1)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 || refused.Load() != 19 {
		t.Fatalf("%d writers won the same key, %d were refused", won.Load(), refused.Load())
	}
}

func TestKVWithoutConditionsWritesAsBefore(t *testing.T) {
	ctx, s := context.Background(), open(t)
	for _, v := range []string{"v1", "v2"} {
		if _, err := s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"k": v}}); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := s.KVRead(ctx, "p", []string{"k"}, nil, time.Time{}); len(got) != 1 || got[0].Value != "v2" {
		t.Fatalf("an unconditional write overwrites, as before: %v", got)
	}
}

func TestKVConditionEdges(t *testing.T) {
	ctx, s := context.Background(), open(t)
	if _, err := s.KVWrite(ctx, "p", map[string]string{"k": "v"}, nil); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]KVPlan{
		"the same key in both, it can never pass": {Set: map[string]string{"x": "1"}, IfAbsent: []string{"k"}, IfValue: map[string]string{"k": "v"}},
		"a bad key in if_absent":                  {Set: map[string]string{"x": "1"}, IfAbsent: []string{"a b"}},
		"a bad key in if_value":                   {Set: map[string]string{"x": "1"}, IfValue: map[string]string{"a//b": "v"}},
		"a guard on a key being deleted":          {Del: []string{"k"}, IfValue: map[string]string{"k": "other"}},
	} {
		p.Project = "p"
		if _, err := s.KVWritePlan(ctx, p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := s.KVWritePlan(ctx, KVPlan{Project: "p", Del: []string{"k"}, IfValue: map[string]string{"k": "v"}}); err != nil {
		t.Fatalf("delete only if it still holds that value: %v", err)
	}
	if got, _ := s.KVRead(ctx, "p", nil, []string{""}, time.Time{}); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	if _, err := s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"x": "1"}, IfAbsent: []string{"k", "k", "gone"}}); err != nil {
		t.Fatalf("duplicate names in if_absent, and a key never written: %v", err)
	}
}

func TestOpenAddsTheColumnsToAnOlderFileAndAnOlderWriterStillWorks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE kv(rid INTEGER PRIMARY KEY, key TEXT NOT NULL, value TEXT NOT NULL, time TEXT NOT NULL, deleted INTEGER NOT NULL);
		INSERT INTO kv(key, value, time, deleted) VALUES('old', 'v', '2026-11-01T00:00:00.000000000Z', 0)`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if got, err := s.KVRead(ctx, "p", []string{"old"}, nil, time.Time{}); err != nil || len(got) != 1 || got[0].Value != "v" {
		t.Fatalf("an old row reads as before: %v %v", got, err)
	}
	if _, err := s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"new": "w"}, IfAbsent: []string{"global/old"}}); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "in project global") {
		t.Fatalf("an old row has no project: it is global, and a refusal says so: %v", err)
	}
	// an older binary inserts naming the columns it knows: the new ones take their defaults
	if _, err := s.w.Exec(`INSERT INTO kv(key, value, time, deleted, expires) VALUES('by-old', 'x', '2026-11-12T00:00:00.000000000Z', 0, '')`); err != nil {
		t.Fatalf("an older binary's insert: %v", err)
	}
}

func TestKVParallelClaimsFromTwoStoresOnOneFileLeaveOneWinnerAndRefusedLosers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "two.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var won, refused, other atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := a
			if i%2 == 1 {
				s = b
			}
			_, err := s.KVWritePlan(context.Background(), KVPlan{Project: "p", Set: map[string]string{"task/adr/9": "mine"}, IfAbsent: []string{"task/adr/9"}})
			switch {
			case err == nil:
				won.Add(1)
			case errors.Is(err, ErrRefused):
				refused.Add(1)
			default:
				other.Add(1)
				t.Log(err)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 || refused.Load() != 29 || other.Load() != 0 {
		t.Fatalf("won %d refused %d other (a raw database error, not a refusal) %d", won.Load(), refused.Load(), other.Load())
	}
}
func TestAddKVColumnToleratesAnotherProcessHavingAddedIt(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE kv(rid INTEGER PRIMARY KEY, key TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	// the column is there when the ALTER runs: the check and the ALTER are two steps, a second process can sit between them
	if _, err := db.Exec(`ALTER TABLE kv ADD COLUMN project TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatal(err)
	}
	if err := addKVColumn(db, "project"); err != nil {
		t.Fatalf("the column already added is the state wanted: %v", err)
	}
	if err := addKVColumn(db, "("); err == nil {
		t.Fatal("any other failure is an error")
	}
}
func TestKVIfValueOnADeletedKeyNamesTheDeleter(t *testing.T) {
	ctx, s := context.Background(), open(t)
	s.KVWrite(ctx, "p", map[string]string{"a": "v"}, nil)
	s.KVWritePlan(ctx, KVPlan{Project: "p", Del: []string{"a"}})
	_, err := s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"b": "w"}, IfValue: map[string]string{"a": "v"}})
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "deleted") || !strings.Contains(err.Error(), "in project p") {
		t.Fatalf("a deleted key: the refusal says deleted, when and by whom: %v", err)
	}
}

// A note belongs to a project (a repository's root commit, shown with its alias): a condition is checked in the scope the key
// would be written to, so one project's claim is not another's, and a global/ key is claimed for every project.
func TestKVConditionsAreCheckedInTheScopeOfTheKey(t *testing.T) {
	ctx, s := context.Background(), open(t)
	if err := s.AddAlias(ctx, "abcdef1234567890abcdef1234567890abcdef12", "shop"); err != nil {
		t.Skipf("no alias API: %v", err)
	}
	shop := "abcdef1234567890abcdef1234567890abcdef12"
	if _, err := s.KVWritePlan(ctx, KVPlan{Project: shop, Set: map[string]string{"task/adr/7": "shop claim"}, IfAbsent: []string{"task/adr/7"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KVWritePlan(ctx, KVPlan{Project: "other", Set: map[string]string{"task/adr/7": "other claim"}, IfAbsent: []string{"task/adr/7"}}); err != nil {
		t.Fatalf("the same key in another project is another note: %v", err)
	}
	_, err := s.KVWritePlan(ctx, KVPlan{Project: shop, Set: map[string]string{"task/adr/7": "again"}, IfAbsent: []string{"task/adr/7"}})
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "[abcdef1] shop") {
		t.Fatalf("a refusal names the project as people read it, short key and alias: %v", err)
	}
	if _, err := s.KVWritePlan(ctx, KVPlan{Project: shop, Set: map[string]string{"global/lock": "shop"}, IfAbsent: []string{"global/lock"}}); err != nil {
		t.Fatal(err)
	}
	_, err = s.KVWritePlan(ctx, KVPlan{Project: "other", Set: map[string]string{"global/lock": "other"}, IfAbsent: []string{"global/lock"}})
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "in project global") {
		t.Fatalf("a global/ key is one note for every project: %v", err)
	}
}
