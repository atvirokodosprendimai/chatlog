package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestKVBodyIsKeptWithTheHeadAndReadOnlyWithFull(t *testing.T) {
	ctx, s := context.Background(), open(t)
	body := "line one\nline two\n" + strings.Repeat("é", 100)
	if _, err := s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"lib/long": "head: the long note -> body"}, Body: map[string]string{"lib/long": body}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.KVReadFull(ctx, "p", []string{"lib/long"}, nil, time.Time{}, false, false)
	if err != nil || len(got) != 1 || got[0].Value != "head: the long note -> body" || !got[0].HasBody || got[0].Body != "" {
		t.Fatalf("without full: the head, has_body, no body: %+v %v", got, err)
	}
	if got, err = s.KVReadFull(ctx, "p", []string{"lib/long"}, nil, time.Time{}, false, true); err != nil || got[0].Body != body {
		t.Fatalf("with full: the body: %+v %v", got, err)
	}
	if got, _ = s.KVRead(ctx, "p", []string{"lib/long"}, nil, time.Time{}); got[0].Body != "" || !got[0].HasBody {
		t.Fatalf("the older readers' call never loads a body: %+v", got)
	}
}

func TestKVBodyIsRefusedWithoutAHeadOrPastTheLimitOrNotText(t *testing.T) {
	ctx, s := context.Background(), open(t)
	bad := map[string]KVPlan{
		"no head":    {Set: map[string]string{"a": "h"}, Body: map[string]string{"b": "text"}},
		"over 8 KiB": {Set: map[string]string{"a": "h"}, Body: map[string]string{"a": strings.Repeat("x", KVBodyMax+1)}},
		"empty":      {Set: map[string]string{"a": "h"}, Body: map[string]string{"a": ""}},
		"not UTF-8":  {Set: map[string]string{"a": "h"}, Body: map[string]string{"a": "\xff\xfe"}},
		"over 8 KiB, multibyte (bytes, not characters)": {Set: map[string]string{"a": "h"}, Body: map[string]string{"a": strings.Repeat("é", KVBodyMax/2+1)}},
		"NUL":          {Set: map[string]string{"a": "h"}, Body: map[string]string{"a": "a\x00b"}},
		"one bad body": {Set: map[string]string{"a": "h", "ok": "v"}, Body: map[string]string{"ok": "fine", "a": ""}},
	}
	for name, p := range bad {
		p.Project = "p"
		if _, err := s.KVWritePlan(ctx, p); !errors.Is(err, ErrRefused) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if got, _ := s.KVRead(ctx, "p", nil, []string{""}, time.Time{}); len(got) != 0 {
		t.Fatalf("a refused plan wrote: %v", got)
	}
	if _, err := s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"a": "h"}, Body: map[string]string{"a": strings.Repeat("x", KVBodyMax)}}); err != nil {
		t.Fatalf("exactly the limit is fine: %v", err)
	}
}

func TestKVRewriteWithoutBodyDropsItButTheHistoryKeepsIt(t *testing.T) {
	ctx, s := context.Background(), open(t)
	s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"a": "v1"}, Body: map[string]string{"a": "old body"}})
	between := time.Now()
	s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"a": "v2"}})
	now, _ := s.KVReadFull(ctx, "p", []string{"a"}, nil, time.Time{}, false, true)
	if now[0].HasBody || now[0].Body != "" {
		t.Fatalf("a write replaces the whole note: %+v", now)
	}
	then, _ := s.KVReadFull(ctx, "p", []string{"a"}, nil, between, false, true)
	if then[0].Body != "old body" {
		t.Fatalf("at: the body as it stood: %+v", then)
	}
}

func TestKVDeletedNoteKeepsItsLastBodyForWithDeleted(t *testing.T) {
	ctx, s := context.Background(), open(t)
	s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"a": "v"}, Body: map[string]string{"a": "the body"}})
	s.KVWrite(ctx, "p", nil, []string{"a"})
	got, err := s.KVReadFull(ctx, "p", []string{"a"}, nil, time.Time{}, true, true)
	if err != nil || len(got) != 1 || got[0].DeletedAt == nil || got[0].Body != "the body" || !got[0].HasBody {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestKVSearchIndexesTheHeadOnly(t *testing.T) {
	ctx, s := context.Background(), open(t)
	s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"a": "the head"}, Body: map[string]string{"a": "SECRETBODYWORD"}})
	pend, err := s.KVPending(ctx, 10)
	if err != nil || len(pend) != 1 || pend[0].Text != "a: the head" {
		t.Fatalf("the vector text is key: head: %+v %v", pend, err)
	}
	live, err := s.KVLatestWith(ctx, "p", []int64{pend[0].ID}, "", false)
	if err != nil || !live[pend[0].ID].HasBody {
		t.Fatalf("a search hit says has_body and carries no body: %+v %v", live, err)
	}
}

func TestKVRewriteWithoutBodyWarnsThatItDropsTheOldOne(t *testing.T) {
	ctx, s := context.Background(), open(t)
	s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"a": "v1", "b": "v1"}, Body: map[string]string{"a": "long", "b": "long"}})
	warn, err := s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"a": "v2", "b": "v2"}, Body: map[string]string{"b": "longer"}})
	if err != nil || len(warn) != 1 || !strings.HasPrefix(warn[0], "a:") || !strings.Contains(warn[0], "body") {
		t.Fatalf("only a, which had a body and gets none, is named: %v %v", warn, err)
	}
	if warn, _ = s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"a": "v3"}}); len(warn) != 0 {
		t.Fatalf("a had no body any more: nothing to warn about: %v", warn)
	}
}

func TestKVTombstoneReadAtATimeTakesTheLastValueNotAfterThatTime(t *testing.T) {
	ctx, s := context.Background(), open(t)
	at := func(h, m int) string { return time.Date(2026, 11, 12, h, m, 0, 0, time.UTC).Format(kvStamp) }
	// the clock stepped back: the delete (09:30) has a later rid than a write stamped 10:00
	for _, r := range []struct {
		value, time, body string
		del               int
	}{{"v1", at(9, 0), "body1", 0}, {"v2", at(10, 0), "body2", 0}, {"", at(9, 30), "", 1}} {
		if _, err := s.w.Exec(`INSERT INTO kv(key, value, time, deleted, body) VALUES('k', ?, ?, ?, ?)`, r.value, r.time, r.del, r.body); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.KVReadFull(ctx, "p", []string{"k"}, nil, time.Date(2026, 11, 12, 9, 45, 0, 0, time.UTC), true, true)
	if err != nil || len(got) != 1 || got[0].Value != "v1" || got[0].Body != "body1" {
		t.Fatalf("as of 09:45 the note had v1: %+v %v", got, err)
	}
}

func TestKVBodyWithExpiryAndTheDeletedPathsSayHasBodyWithoutLoadingIt(t *testing.T) {
	ctx, s := context.Background(), open(t)
	if _, err := s.KVWritePlan(ctx, KVPlan{Project: "p", Set: map[string]string{"a": "h"}, Body: map[string]string{"a": "text"}, Expires: map[string]string{"a": "1h"}}); err != nil {
		t.Fatal(err)
	}
	s.KVWrite(ctx, "p", nil, []string{"a"})
	got, err := s.KVReadFull(ctx, "p", []string{"a"}, nil, time.Time{}, true, false)
	if err != nil || len(got) != 1 || !got[0].HasBody || got[0].Body != "" || got[0].ExpiresAt == nil {
		t.Fatalf("a deleted note, not full: has_body, no body, its expiry: %+v %v", got, err)
	}
	rids := []int64{1}
	live, err := s.KVLatestWith(ctx, "p", rids, "", true)
	if err != nil || !live[1].HasBody || live[1].DeletedAt == nil {
		t.Fatalf("a search hit on a deleted note says has_body: %+v %v", live, err)
	}
}
