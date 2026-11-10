// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A PostToolUse looks its row up by id at every tool call: it must use the index, not scan the table.
func TestFinishToolUsesTheIndex(t *testing.T) {
	s := open(t)
	rows, err := s.r.Query(`EXPLAIN QUERY PLAN SELECT id, time FROM events WHERE tool_use_id=? AND tool_use_id <> '' AND event='PreToolUse'
		AND duration_ms IS NULL ORDER BY id DESC LIMIT 1`, "x")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		rows.Scan(&id, &parent, &notused, &detail)
		plan = append(plan, detail)
	}
	if p := strings.Join(plan, " | "); !strings.Contains(p, "events_tool_use") {
		t.Fatalf("plan does not use the index: %s", p)
	}
}

func TestLastInputAndHasEventAreOfOneSession(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	for i, e := range []Event{
		{Event: "Check", Session: "a", Input: "first"}, {Event: "Check", Session: "a", Input: ""},
		{Event: "Check", Session: "b", Input: "other"}, {Event: "KarmaLib", Session: "a", Input: "hosts"},
	} {
		e.Time = now.Add(time.Duration(i) * time.Second)
		if _, err := s.AddEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if in, ok, err := s.LastInput(ctx, "a", "Check"); in != "" || !ok || err != nil {
		t.Errorf("the latest row, empty as it is: %q %v %v", in, ok, err)
	}
	if in, ok, _ := s.LastInput(ctx, "b", "Check"); in != "other" || !ok {
		t.Errorf("%q %v", in, ok)
	}
	if _, ok, _ := s.LastInput(ctx, "c", "Check"); ok {
		t.Error("a session with no row has none")
	}
	for _, c := range []struct {
		session, event, input string
		want                  bool
	}{{"a", "KarmaLib", "hosts", true}, {"b", "KarmaLib", "hosts", false}, {"a", "KarmaLib", "other", false}, {"a", "Check", "hosts", false}} {
		if got, err := s.HasEvent(ctx, c.session, c.event, c.input); got != c.want || err != nil {
			t.Errorf("%+v: %v %v", c, got, err)
		}
	}
}
