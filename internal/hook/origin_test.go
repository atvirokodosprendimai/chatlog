// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A subagent's window is pushed by the same hooks as the user's own; the compaction digest must still hold only the latter.
func TestSubagentWorkNeverReachesTheWakeDigestAfterACompaction(t *testing.T) {
	r := newRig(t)
	main := filepath.Join(r.dir, "projects", "-u-webshop", "s1.jsonl")
	sub := filepath.Join(r.dir, "projects", "-u-webshop", "s1", "subagents", "agent-a1.jsonl")
	for p, row := range map[string][2]string{
		main: {"what the user asked about the gate", "the gate is green"},
		sub:  {"brief written by the main agent for the subagent", "the subagent rewrote the gate script"},
	} {
		os.MkdirAll(filepath.Dir(p), 0o700)
		lines := fmt.Sprintf(`{"type":"user","uuid":"u-%[1]s","sessionId":"s1","cwd":"/u/webshop","timestamp":"2026-11-08T09:00:00Z","origin":{"kind":"human"},"message":{"content":%[2]q}}`+"\n"+
			`{"type":"assistant","uuid":"a-%[1]s","sessionId":"s1","timestamp":"2026-11-08T09:00:05Z","message":{"content":[{"type":"text","text":%[3]q}]}}`+"\n", filepath.Base(p), row[0], row[1])
		if err := os.WriteFile(p, []byte(lines), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.run("SubagentStop", fmt.Sprintf(`{"session_id":"s1","cwd":"/u/webshop","transcript_path":%q,"agent_transcript_path":%q}`, main, sub)); err != nil {
		t.Fatal(err)
	}
	got, err := r.run("SessionStart", `{"session_id":"s1","cwd":"/u/webshop","source":"compact"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "what the user asked about the gate") {
		t.Fatalf("the user's own exchange is missing:\n%s", got)
	}
	if strings.Contains(got, "subagent") || strings.Contains(got, "brief written") {
		t.Fatalf("a subagent's work is in the user's context after a compaction:\n%s", got)
	}
}
