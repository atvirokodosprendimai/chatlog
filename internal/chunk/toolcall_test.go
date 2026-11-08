// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package chunk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atvirokodosprendimai/chatlog/internal/transcript"
)

// A stored exchange keeps each tool call's start, duration and failure, read from the transcript's own times.
func TestToolCallsKeepTimeDurationAndError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	lines := strings.Join([]string{
		`{"type":"user","uuid":"u1","sessionId":"s","timestamp":"2026-11-08T09:00:00Z","origin":{"kind":"human"},"message":{"content":"run it"}}`,
		`{"type":"assistant","uuid":"a1","sessionId":"s","timestamp":"2026-11-08T09:00:01Z","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"make"}}]}}`,
		`{"type":"user","uuid":"r1","sessionId":"s","timestamp":"2026-11-08T09:00:03.5Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"exit 2"}]}}`,
		`{"type":"assistant","uuid":"a2","sessionId":"s","timestamp":"2026-11-08T09:00:04Z","message":{"content":[{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"/x"}}]}}`,
		`{"type":"user","uuid":"r2","sessionId":"s","timestamp":"2026-11-08T09:00:04.2Z","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"ok"}]}}`,
		`{"type":"assistant","uuid":"a3","sessionId":"s","timestamp":"2026-11-08T09:00:05Z","message":{"content":[{"type":"text","text":"make failed"}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(p, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	tail, err := transcript.Read(p, 0, func(transcript.Exchange, int64) error { return nil })
	if err != nil || tail.Open == nil {
		t.Fatalf("%v %+v", err, tail)
	}
	full := Build(*tail.Open, DefaultLimit).Full
	for _, want := range []string{"[tool Bash 09:00:01Z 2.5s error]\nmake", "[output error]\nexit 2", "[tool Read 09:00:04Z 200ms]", "[output]\nok"} {
		if !strings.Contains(full, want) {
			t.Fatalf("missing %q in:\n%s", want, full)
		}
	}
}

// The history read from the transcript keeps no dotenv content either: only the file's name.
func TestDotenvCallsKeepNoContentInTheHistory(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	lines := strings.Join([]string{
		`{"type":"user","uuid":"u1","sessionId":"s","timestamp":"2026-11-08T09:00:00Z","origin":{"kind":"human"},"message":{"content":"show env"}}`,
		`{"type":"assistant","uuid":"a1","sessionId":"s","timestamp":"2026-11-08T09:00:01Z","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"cat .env && echo X=planted1"}}]}}`,
		`{"type":"user","uuid":"r1","sessionId":"s","timestamp":"2026-11-08T09:00:02Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"DB_HOST=db1\nSMTP_LOGIN=plainvalue2"}]}}`,
		`{"type":"assistant","uuid":"a2","sessionId":"s","timestamp":"2026-11-08T09:00:03Z","message":{"content":[{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"/app/.env.local"}}]}}`,
		`{"type":"user","uuid":"r2","sessionId":"s","timestamp":"2026-11-08T09:00:04Z","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"MAIL_FROM=plainvalue3"}]}}`,
	}, "\n") + "\n"
	os.WriteFile(p, []byte(lines), 0o600)
	tail, err := transcript.Read(p, 0, func(transcript.Exchange, int64) error { return nil })
	if err != nil || tail.Open == nil {
		t.Fatalf("%v", err)
	}
	full := Build(*tail.Open, DefaultLimit).Full
	for _, leak := range []string{"planted1", "plainvalue2", "plainvalue3", "db1"} {
		if strings.Contains(full, leak) {
			t.Fatalf("%q reached the history:\n%s", leak, full)
		}
	}
	if !strings.Contains(full, ".env.local") || !strings.Contains(full, "[touches .env") {
		t.Fatalf("the file names should stay findable:\n%s", full)
	}
}
