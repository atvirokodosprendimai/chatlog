// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	humanTyped = `{"type":"user","uuid":"u1","sessionId":"s1","cwd":"/repo","timestamp":"2026-10-10T10:00:00Z","origin":{"kind":"human"},"message":{"role":"user","content":"kur TEI?"}}`
	call       = `{"type":"assistant","uuid":"a1","sessionId":"s1","timestamp":"2026-10-10T10:00:01Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hidden"},{"type":"tool_use","name":"Bash","input":{"command":"ls -l"}}]}}`
	result     = `{"type":"user","uuid":"r1","sessionId":"s1","timestamp":"2026-10-10T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","content":"file.txt"}]}}`
	mid        = `{"type":"assistant","uuid":"a2","sessionId":"s1","timestamp":"2026-10-10T10:00:03Z","message":{"role":"assistant","content":[{"type":"text","text":"looking"}]}}`
	answer     = `{"type":"assistant","uuid":"a3","sessionId":"s1","timestamp":"2026-10-10T10:00:04Z","message":{"role":"assistant","content":[{"type":"text","text":"TEI yra ten"}]}}`
	human2     = `{"type":"user","uuid":"u2","sessionId":"s1","cwd":"/repo","timestamp":"2026-10-10T11:00:00Z","origin":{"kind":"human"},"message":{"role":"user","content":"status"}}`
)

func write(t *testing.T, name string, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func all(t *testing.T, p string) ([]Exchange, Tail) {
	t.Helper()
	var got []Exchange
	tail, err := Read(p, 0, func(e Exchange, _ int64) error { got = append(got, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if tail.Open != nil {
		got = append(got, *tail.Open)
	}
	return got, tail
}

func TestOneExchange(t *testing.T) {
	got, tail := all(t, write(t, "a.jsonl", humanTyped, call, result, mid, answer, human2))
	if len(got) != 2 {
		t.Fatalf("%d exchanges", len(got))
	}
	e := got[0]
	if e.UUID != "u1" || e.Words != "kur TEI?" || e.Answer != "TEI yra ten" || e.Cwd != "/repo" || !e.Closed {
		t.Fatalf("%+v", e)
	}
	if len(e.Parts) != 3 || e.Parts[0].Kind != ToolCall || e.Parts[0].Name != "Bash" || e.Parts[0].Text != "ls -l" ||
		e.Parts[1].Kind != ToolOutput || e.Parts[1].Text != "file.txt" || e.Parts[2].Kind != Text {
		t.Fatalf("%+v", e.Parts)
	}
	if got[1].Closed || tail.Open == nil || tail.Open.UUID != "u2" {
		t.Fatalf("open %+v", tail.Open)
	}
}

func TestMachineMessagesAreNotM(t *testing.T) {
	for name, row := range map[string]string{
		"notification by origin": `{"type":"user","uuid":"m","origin":{"kind":"task-notification"},"message":{"content":"<task-notification>x</task-notification>"}}`,
		"notification no origin": `{"type":"user","uuid":"m","message":{"content":"<task-notification>x</task-notification>"}}`,
		"agent message":          `{"type":"user","uuid":"m","message":{"content":"<agent-message from=\"a\">x</agent-message>"}}`,
		"meta hook":              `{"type":"user","uuid":"m","isMeta":true,"message":{"content":"Stop hook feedback: x"}}`,
		"cron":                   `{"type":"user","uuid":"m","scheduledTaskId":"c1","message":{"content":"every five minutes"}}`,
		"compact":                `{"type":"user","uuid":"m","isCompactSummary":true,"message":{"content":"This session is being continued"}}`,
		"sidechain brief":        `{"type":"user","uuid":"m","isSidechain":true,"message":{"content":"Chunk 7 of 8"}}`,
		"coordinator queued":     `{"type":"attachment","uuid":"m","attachment":{"type":"queued_command","prompt":"Addition from M","origin":{"kind":"coordinator"},"isMeta":true}}`,
		"peer queued":            `{"type":"attachment","uuid":"m","attachment":{"type":"queued_command","prompt":"hello","origin":{"kind":"peer"},"commandMode":"prompt"}}`,
		"hook attachment":        `{"type":"attachment","uuid":"m","attachment":{"type":"hook_additional_context","content":"x"}}`,
		"interrupted":            `{"type":"user","uuid":"m","message":{"content":"[Request interrupted by user]"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			got, _ := all(t, write(t, "a.jsonl", row))
			if len(got) != 0 {
				t.Fatalf("became M: %+v", got[0])
			}
			got, _ = all(t, write(t, "a.jsonl", humanTyped, row, answer))
			if len(got) != 1 || got[0].Answer != "TEI yra ten" {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestReminderStrippedFromWords(t *testing.T) {
	row := `{"type":"user","uuid":"u","origin":{"kind":"human"},"message":{"content":"labas<system-reminder>noise</system-reminder>"}}`
	got, _ := all(t, write(t, "a.jsonl", row))
	if got[0].Words != "labas" {
		t.Fatalf("%q", got[0].Words)
	}
}

func TestSlashCommandKeepsItsArgs(t *testing.T) {
	row := `{"type":"user","uuid":"u","origin":{"kind":"human"},"message":{"content":"<command-message>am</command-message> <command-name>/am</command-name> <command-args>juridiniai</command-args>"}}`
	got, _ := all(t, write(t, "a.jsonl", row))
	if got[0].Words != "/am juridiniai" {
		t.Fatalf("%q", got[0].Words)
	}
}

// M typing while a turn runs: a human queued_command closes the exchange and opens its own.
func TestMidTurnHumanOpensOwnExchange(t *testing.T) {
	q := `{"type":"attachment","uuid":"q1","sessionId":"s1","timestamp":"2026-10-10T10:00:02Z","attachment":{"type":"queued_command","prompt":"pala","origin":{"kind":"human"},"commandMode":"prompt"}}`
	got, _ := all(t, write(t, "a.jsonl", humanTyped, call, q, answer))
	if len(got) != 2 || got[0].Words != "kur TEI?" || got[1].Words != "pala" || got[1].Answer != "TEI yra ten" {
		t.Fatalf("%+v", got)
	}
}

// M typing into a subagent's window: his words, marked with the agent.
func TestSubagentQueuedIsMWithAgent(t *testing.T) {
	brief := `{"type":"user","uuid":"b","isSidechain":true,"agentId":"ag1","message":{"content":"Do the work"}}`
	q := `{"type":"attachment","uuid":"q1","isSidechain":true,"agentId":"ag1","sessionId":"s1","timestamp":"2026-10-10T10:00:02Z","attachment":{"type":"queued_command","prompt":"paziure?","origin":{"kind":"human"},"isMeta":true}}`
	work := `{"type":"assistant","uuid":"w","isSidechain":true,"agentId":"ag1","message":{"content":[{"type":"text","text":"ziuriu"}]}}`
	got, _ := all(t, write(t, "s1/subagents/agent-ag1.jsonl", brief, work, q, strings.Replace(work, `"uuid":"w"`, `"uuid":"w2"`, 1)))
	if len(got) != 1 || got[0].Words != "paziure?" || got[0].Agent != "ag1" || got[0].Answer != "ziuriu" {
		t.Fatalf("%+v", got)
	}
}

// Sidechain rows inside a main file are the agent's, not the session's.
func TestSidechainInMainFileIgnored(t *testing.T) {
	work := `{"type":"assistant","uuid":"w","isSidechain":true,"message":{"content":[{"type":"text","text":"agent text"}]}}`
	got, _ := all(t, write(t, "a.jsonl", humanTyped, work))
	if len(got) != 1 || got[0].Answer != "" {
		t.Fatalf("%+v", got)
	}
}

func TestSecretsRedactedBeforeLeavingParser(t *testing.T) {
	row := `{"type":"user","uuid":"u","origin":{"kind":"human"},"message":{"content":"sk-ant-oat01-INVENTEDxxxxxxxxxxxxxxxxx"}}`
	c := `{"type":"assistant","uuid":"a","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"curl -H 'Authorization: Bearer inventedtoken123'"}}]}}`
	r := `{"type":"user","uuid":"r","message":{"content":[{"type":"tool_result","content":"PASSWORD=inventedpass"}]}}`
	got, _ := all(t, write(t, "a.jsonl", row, c, r))
	joined := got[0].Words + got[0].Parts[0].Text + got[0].Parts[1].Text
	for _, s := range []string{"INVENTED", "inventedtoken", "inventedpass"} {
		if strings.Contains(joined, s) {
			t.Fatalf("%q left in %q", s, joined)
		}
	}
}

func TestDuplicateQueuedEchoIsOneMessage(t *testing.T) {
	q := `{"type":"attachment","uuid":"q1","timestamp":"2026-10-10T10:00:00Z","attachment":{"type":"queued_command","prompt":"kur TEI?","origin":{"kind":"human"},"commandMode":"prompt"}}`
	got, _ := all(t, write(t, "a.jsonl", q, humanTyped, answer))
	if len(got) != 1 {
		t.Fatalf("%d", len(got))
	}
}

func TestHalfWrittenLastLineWaits(t *testing.T) {
	p := write(t, "a.jsonl", humanTyped, answer)
	full, _ := os.ReadFile(p)
	half := `{"type":"assistant","uuid":"a9","message":{"content":[{"type":"text","te`
	if err := os.WriteFile(p, append(full, half...), 0o600); err != nil {
		t.Fatal(err)
	}
	got, tail := all(t, p)
	if len(got) != 1 || tail.ReadTo != int64(len(full)) {
		t.Fatalf("%d readTo %d of %d", len(got), tail.ReadTo, len(full))
	}
}

// A corrupt complete line is passed over, it must not stop the file forever.
func TestCorruptLineSkipped(t *testing.T) {
	got, _ := all(t, write(t, "a.jsonl", humanTyped, "{not json", answer))
	if len(got) != 1 || got[0].Answer != "TEI yra ten" {
		t.Fatalf("%+v", got)
	}
}

// The cursor stands at the line that opens the open exchange, so a later read rebuilds it whole.
func TestCursorRebuildsOpenExchange(t *testing.T) {
	p := write(t, "a.jsonl", humanTyped, answer, human2)
	_, tail := all(t, p)
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"type":"assistant","uuid":"a7","message":{"content":[{"type":"text","text":"veikia"}]}}` + "\n")
	f.Close()
	var closed int
	t2, err := Read(p, tail.Cursor, func(Exchange, int64) error { closed++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if closed != 0 || t2.Open == nil || t2.Open.UUID != "u2" || t2.Open.Answer != "veikia" {
		t.Fatalf("closed %d open %+v", closed, t2.Open)
	}
}
