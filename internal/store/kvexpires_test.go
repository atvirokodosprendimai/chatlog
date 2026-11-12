package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func byKey(kv []KV) map[string]KV {
	m := map[string]KV{}
	for _, e := range kv {
		m[e.Key] = e
	}
	return m
}

func TestKVExpiryMarksAnyKeyStaleOnceItHasPassed(t *testing.T) {
	ctx, s := context.Background(), open(t)
	if _, err := s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"fact/x": "v", "fact/y": "v", "now/z": "v"},
		Expires: map[string]string{"fact/x": "2h", "now/z": "3d"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.KVRead(ctx, "p", nil, []string{""}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	m := byKey(got)
	if m["fact/x"].ExpiresAt == nil || m["fact/y"].ExpiresAt != nil {
		t.Fatalf("expires_at: x %v, y %v: want x only", m["fact/x"].ExpiresAt, m["fact/y"].ExpiresAt)
	}
	if m["fact/x"].Stale || m["now/z"].Stale {
		t.Fatalf("stale before the expiry: %+v", m)
	}
	// read as of a time past the 2h expiry of x: the expiry decides, for a fact/ key
	got, err = s.KVRead(ctx, "p", nil, []string{""}, time.Now().Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	m = byKey(got)
	if !m["fact/x"].Stale {
		t.Errorf("fact/x not stale after its expiry")
	}
	if m["fact/y"].Stale || m["now/z"].Stale {
		t.Errorf("stale without cause: y %v, z %v", m["fact/y"].Stale, m["now/z"].Stale)
	}
	// 13 h on, now/z is past the 12 h rule but its own 3d expiry is the one that counts
	got, _ = s.KVRead(ctx, "p", nil, []string{"now/z"}, time.Now().Add(13*time.Hour))
	if got[0].Stale {
		t.Errorf("now/z stale at 13h though its expiry is 3d away")
	}
}

func TestKVExpiryRefusesWhatIsNotAFutureTimeOrKeyOfSet(t *testing.T) {
	ctx, s := context.Background(), open(t)
	for _, c := range []struct {
		name string
		exp  map[string]string
	}{
		{"not a span", map[string]string{"a": "soon"}},
		{"past", map[string]string{"a": "2020-01-01T00:00:00Z"}},
		{"not in set", map[string]string{"b": "1h"}},
	} {
		_, err := s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"a": "v"}, Expires: c.exp})
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%s: err %v, want refused", c.name, err)
		}
	}
	if got, _ := s.KVRead(ctx, "p", []string{"a"}, nil, time.Time{}); len(got) != 0 {
		t.Fatalf("a refused plan wrote %+v", got)
	}
}

func TestKVReadWithDeletedReturnsTheLastValueAndWhenItWasDeleted(t *testing.T) {
	ctx, s := context.Background(), open(t)
	s.KVWrite(ctx, "p", map[string]string{"now/a": "first", "fact/b": "keep"}, nil)
	s.KVWrite(ctx, "p", map[string]string{"now/a": "second"}, nil)
	s.KVWrite(ctx, "p", nil, []string{"now/a", "never/had"})
	plain, _ := s.KVRead(ctx, "p", nil, []string{""}, time.Time{})
	if _, ok := byKey(plain)["now/a"]; ok || len(plain) != 1 {
		t.Fatalf("plain read %+v: want fact/b only", plain)
	}
	got, err := s.KVReadFull(ctx, "p", nil, []string{""}, time.Time{}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	m := byKey(got)
	if len(got) != 2 || m["now/a"].Value != "second" || m["now/a"].DeletedAt == nil || m["now/a"].Stale {
		t.Fatalf("with deleted %+v: want now/a at 'second' with deleted_at, and no never/had", got)
	}
	if m["fact/b"].DeletedAt != nil {
		t.Fatalf("a live key has deleted_at: %+v", m["fact/b"])
	}
}

func TestKVLatestWithDeletedFindsADeletedNoteByItsLastValue(t *testing.T) {
	ctx, s := context.Background(), open(t)
	s.KVWrite(ctx, "p", map[string]string{"fact/gone": "was true", "fact/here": "is true"}, nil)
	p, _ := s.KVPending(ctx, 10)
	s.KVWrite(ctx, "p", nil, []string{"fact/gone"})
	var rids []int64
	for _, e := range p {
		rids = append(rids, e.ID)
	}
	plain, _ := s.KVLatestWith(ctx, "p", rids, "fact/", false)
	if len(plain) != 1 {
		t.Fatalf("plain %+v: want fact/here only", plain)
	}
	with, err := s.KVLatestWith(ctx, "p", rids, "fact/", true)
	if err != nil {
		t.Fatal(err)
	}
	var gone *KV
	for _, e := range with {
		if e.Key == "fact/gone" {
			gone = &e
		}
	}
	if len(with) != 2 || gone == nil || gone.Value != "was true" || gone.DeletedAt == nil {
		t.Fatalf("with deleted %+v: want fact/gone at 'was true' with deleted_at", with)
	}
}

func TestOpenAddsTheExpiresColumnToAnOldFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// make the file look as it did before expires: drop the column, keep a row
	for _, q := range []string{
		`INSERT INTO kv(key, value, time, deleted) VALUES('fact/old','v','2026-01-01T00:00:00.000000000Z',0)`,
		`ALTER TABLE kv DROP COLUMN expires`,
	} {
		if _, err := s.w.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.KVRead(context.Background(), "p", []string{"fact/old"}, nil, time.Time{})
	if err != nil || len(got) != 1 || got[0].ExpiresAt != nil {
		t.Fatalf("old row after migration: %+v, %v", got, err)
	}
}
