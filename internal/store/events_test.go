// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"strings"
	"testing"
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
