// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package hook

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/config"
	"github.com/atvirokodosprendimai/chatlog/internal/search"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

func (r *rig) kv(pairs ...string) {
	r.t.Helper()
	set := map[string]string{}
	for i := 0; i < len(pairs); i += 2 {
		set[pairs[i]] = pairs[i+1]
	}
	if _, err := r.d.Store.KVWrite(context.Background(), set, nil); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) link(s, o string) {
	r.t.Helper()
	if err := r.d.Store.KVLink(context.Background(), []store.Link{{S: s, P: "applies_to", O: o}}, nil); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) off(pieces ...string) { r.d.Config.Hooks.Off = pieces }

// transcript writes a fixture transcript and returns its path.
func (r *rig) transcript(lines ...string) string {
	r.t.Helper()
	p := filepath.Join(r.dir, "t.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		r.t.Fatal(err)
	}
	return p
}

func (r *rig) prompt(session, prompt, transcript string) (string, error) {
	return r.run("UserPromptSubmit", fmt.Sprintf(`{"session_id":%q,"prompt":%q,"transcript_path":%q}`, session, prompt, transcript))
}

func (r *rig) stop(session, transcript string) {
	r.t.Helper()
	if _, err := r.run("Stop", fmt.Sprintf(`{"session_id":%q,"transcript_path":%q}`, session, transcript)); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) pre(session, tool, input string) (string, error) {
	return r.run("PreToolUse", fmt.Sprintf(`{"session_id":%q,"tool_name":%q,"tool_use_id":"u","tool_input":%s}`, session, tool, input))
}

func (r *rig) count(event string) int {
	n := 0
	for _, e := range r.events() {
		if e.Event == event {
			n++
		}
	}
	return n
}

const (
	userLine  = `{"type":"user","message":{"role":"user","content":"fix the build"}}`
	shortLine = `{"type":"user","message":{"role":"user","content":"why?"}}`
	editLine  = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"a"}}],"usage":{"input_tokens":2,"cache_read_input_tokens":129000,"cache_creation_input_tokens":998}}}`
)

func TestPromptPartsComeInTheirOrder(t *testing.T) {
	r := newRig(t)
	r.kv("hook/prompt/02", "second line", "hook/prompt/01", "first line", "sit/rel", "release | check the gate first", "sit/rel/words/1", "release")
	r.kv("rule/gate", "run the gate first")
	r.link("rule/gate", "sit/rel")
	r.d.Recall = func(context.Context, string, int) ([]search.KVHit, error) {
		return []search.KVHit{{KV: store.KV{Key: "fact/gate", Value: "the gate is X"}, Score: 0.6}}, nil
	}
	tp := r.transcript(userLine, editLine)
	r.stop("s1", tp)
	got, err := r.prompt("s1", "when is the release gate run", tp)
	if err != nil {
		t.Fatal(err)
	}
	var at []int
	for _, w := range []string{"now 2026-11-08T09:00:00Z UTC", "hook/prompt/01: first line\nhook/prompt/02: second line", "last turn missed: state changed (Edit x1)", "SLEEP NOW, context: 130k of ~200k", "0.60 fact/gate: the gate is X", "situation sit/rel release | check the gate first | rules: rule/gate"} {
		i := strings.Index(got, w)
		if i < 0 {
			t.Fatalf("%q missing from\n%s", w, got)
		}
		at = append(at, i)
	}
	for i := 1; i < len(at); i++ {
		if at[i] <= at[i-1] {
			t.Fatalf("order: time, notes, misses, context, recall; got offsets %v in\n%s", at, got)
		}
	}
}

func TestMissesAreShownOnceThenCleared(t *testing.T) {
	r := newRig(t)
	tp := r.transcript(userLine, editLine)
	r.stop("s1", tp)
	if got, _ := r.prompt("s1", "next one please now", tp); !strings.Contains(got, "last turn missed: state changed (Edit x1) and no kv_write in the turn") {
		t.Fatalf("not shown: %q", got)
	}
	if got, _ := r.prompt("s1", "and again please now", tp); strings.Contains(got, "last turn missed") {
		t.Fatalf("shown twice: %q", got)
	}
	r.stop("s1", tp)
	if got, _ := r.prompt("other", "a session of its own", tp); strings.Contains(got, "last turn missed") {
		t.Fatalf("crossed sessions: %q", got)
	}
}

func TestACleanTurnSupersedesAMissNotYetShown(t *testing.T) {
	r := newRig(t)
	longUser := `{"type":"user","message":{"role":"user","content":"please fix the failing build and explain what broke"}}`
	r.stop("s1", r.transcript(longUser, editLine))
	r.stop("s1", r.transcript(longUser, editLine, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__chatlog__kv_write","input":{}}]}}`))
	if got, _ := r.prompt("s1", "show me what you did", ""); strings.Contains(got, "last turn missed") {
		t.Fatalf("%q", got)
	}
}

func TestCheckUsesTheUsersRulesAndNeverBlocks(t *testing.T) {
	r := newRig(t)
	r.kv("hook/check/rule/1", `Bash :: ^cat\s+\S+\.go :: file read through the shell`, "hook/check/change/1", `git push`)
	tp := r.transcript(userLine, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"cat main.go"}},{"type":"tool_use","name":"Bash","input":{"command":"git push"}}]}}`)
	out, err := r.run("Stop", fmt.Sprintf(`{"session_id":"s1","transcript_path":%q}`, tp))
	if err != nil || out != "" {
		t.Fatalf("a Stop prints nothing and does not fail: %q %v", out, err)
	}
	got, _ := r.prompt("s1", "anything else for me", tp)
	if !strings.Contains(got, "state changed (Bash x1)") || !strings.Contains(got, "file read through the shell") {
		t.Fatalf("%q", got)
	}
}

func TestShortPromptWithoutRecallIsAMiss(t *testing.T) {
	r := newRig(t)
	r.stop("s1", r.transcript(shortLine, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`))
	if got, _ := r.prompt("s1", "and now the next prompt", ""); !strings.Contains(got, "a short prompt was answered with tool calls but no chat_search") {
		t.Fatalf("%q", got)
	}
}

func TestCheckSurvivesAMissingOrEmptyTranscript(t *testing.T) {
	r := newRig(t)
	for _, tp := range []string{"", filepath.Join(r.dir, "nope.jsonl"), r.transcript(`{"type":"assistant","message":{"content":[]}}`)} {
		if _, err := r.run("Stop", fmt.Sprintf(`{"session_id":"s1","transcript_path":%q}`, tp)); err != nil {
			t.Errorf("%q: %v", tp, err)
		}
	}
	if r.count(EventCheck) != 0 {
		t.Error("nothing audited, nothing stored")
	}
}

func TestSubagentStopIsNotAudited(t *testing.T) {
	r := newRig(t)
	if _, err := r.run("SubagentStop", fmt.Sprintf(`{"session_id":"s1","transcript_path":%q}`, r.transcript(userLine, editLine))); err != nil {
		t.Fatal(err)
	}
	if r.count(EventCheck) != 0 {
		t.Error("a subagent's turn is not the session's")
	}
}

func TestContextMeterFromTheTranscript(t *testing.T) {
	r := newRig(t)
	tp := r.transcript(userLine, strings.Replace(editLine, "129000", "49000", 1))
	got, _ := r.prompt("s1", "how full are we", tp)
	if !strings.Contains(got, "context: 50k of ~200k (25%), ~150k left; sleep at 60% (~120k).") || strings.Contains(got, "SLEEP") {
		t.Fatalf("%q", got)
	}
	r.d.Config.Hooks.ContextWindow, r.d.Config.Hooks.Sleep = 100_000, 0.5
	if got, _ := r.prompt("s1", "how full are we", tp); !strings.Contains(got, "SLEEP NOW, context: 50k of ~100k (50%)") {
		t.Fatalf("window and sleep come from the config: %q", got)
	}
	if got, _ := r.prompt("s1", "how full are we", ""); strings.Contains(got, "context:") {
		t.Fatalf("no transcript, no meter: %q", got)
	}
}

func TestRecallNeedsWordsAndSkipsWhenTheServerDoesNot(t *testing.T) {
	r := newRig(t)
	r.kv("sit/g", "what | move", "sit/g/words/1", "gate")
	calls := 0
	r.d.Recall = func(ctx context.Context, q string, n int) ([]search.KVHit, error) {
		calls++
		return nil, errors.New("the chatlog server does not answer")
	}
	if _, err := r.prompt("s1", "gate", ""); err != nil || calls != 0 {
		t.Fatalf("a one-word prompt searches nothing: %d %v", calls, err)
	}
	got, err := r.prompt("s1", "what about the gate today", "")
	if calls != 1 || err == nil || !strings.Contains(err.Error(), "prompt recall skipped after") {
		t.Fatalf("a failed search is logged, not raised to the session: %d %v", calls, err)
	}
	if !strings.Contains(got, "situation sit/g what | move") || strings.Contains(got, "does not answer") {
		t.Fatalf("the situation does not wait on the search, and the session never sees its failure: %q", got)
	}
}

func TestRecallPrintsStaleOnesWithTheCheckLine(t *testing.T) {
	r := newRig(t)
	r.d.Recall = func(context.Context, string, int) ([]search.KVHit, error) {
		return []search.KVHit{
			{KV: store.KV{Key: "map/code", Value: "x"}, Score: 0.9},
			{KV: store.KV{Key: "now/job", Value: "running", Stale: true, Age: "20h"}, Score: 0.5},
			{KV: store.KV{Key: "fact/low", Value: "x"}, Score: 0.3},
		}, nil
	}
	got, _ := r.prompt("s1", "is the job still running", "")
	for _, w := range []string{"0.50 now/job (STALE 20h): running", "check now/job against the source", "rewrite or delete"} {
		if !strings.Contains(got, w) {
			t.Errorf("%q missing from %q", w, got)
		}
	}
	if strings.Contains(got, "map/code") || strings.Contains(got, "fact/low") {
		t.Errorf("%q", got)
	}
}

func TestBadRulesAreReportedAtThePrompt(t *testing.T) {
	r := newRig(t)
	r.kv("sit/g/sig/1", `Bash :: (?=x)`, "hook/check/rule/1", "two :: parts")
	got, _ := r.prompt("s1", "anything at all here", "")
	if !strings.Contains(got, "kv rules NOT read") || !strings.Contains(got, "sit/g/sig/1") || !strings.Contains(got, "hook/check/rule/1") {
		t.Fatalf("%q", got)
	}
}

func TestKarmaRecognisesAToolCallAndCountsIt(t *testing.T) {
	r := newRig(t)
	r.kv("sit/push", "a push | ask first", "sit/push/sig/1", `Bash :: git +push`, "rule/ask", "ask before a push")
	r.link("rule/ask", "sit/push")
	got, err := r.pre("s1", "Bash", `{"command":"cd x && git push origin main"}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "karma (situation recognised; its move, then its rules by key): sit/push a push | ask first | rules: rule/ask"; got != want {
		t.Fatalf("%q, want %q", got, want)
	}
	if got, _ := r.pre("s1", "Bash", `{"command":"ls"}`); got != "" {
		t.Fatalf("a call that fits nothing is silent: %q", got)
	}
	var hits []store.Event
	for _, e := range r.events() {
		if e.Event == EventKarma {
			hits = append(hits, e)
		}
	}
	if len(hits) != 1 || hits[0].Input != "sit/push" || hits[0].Tool != "Bash" || hits[0].Session != "s1" {
		t.Fatalf("one row per recognition: %+v", hits)
	}
	if r.count("PreToolUse") != 2 {
		t.Fatalf("the tool rows are still written: %d", r.count("PreToolUse"))
	}
}

func TestKarmaMatchesTheCommandNotItsJSON(t *testing.T) {
	r := newRig(t)
	r.kv("sit/t", "tests | x", "sit/t/sig/1", `Bash :: ^go test`, "sit/e", "edit | x", "sit/e/sig/1", `Edit :: "file_path":"[^"]*\.env"`)
	if got, _ := r.pre("s1", "Bash", `{"command":"cd x\ngo test ./..."}`); !strings.Contains(got, "sit/t") {
		t.Errorf("a line start inside the command: %q", got)
	}
	if got, _ := r.pre("s1", "Edit", `{"file_path":"/a/b.env","old_string":"a"}`); !strings.Contains(got, "sit/e") {
		t.Errorf("another tool is matched on its input's JSON: %q", got)
	}
}

func TestKarmaLibraryComesOncePerSession(t *testing.T) {
	r := newRig(t)
	r.kv("sit/prod", "prod | read first", "sit/prod/sig/1", `Bash :: ssh`, "legend/lib/hosts", "the host commands", "lib/hosts/1", "ssh h1 uptime", "lib/hosts/2", "ssh h2 df")
	r.link("legend/lib/hosts", "sit/prod")
	first, _ := r.pre("s1", "Bash", `{"command":"ssh h1"}`)
	if !strings.Contains(first, "lib/hosts (loaded once this session): lib/hosts/1: ssh h1 uptime ;; lib/hosts/2: ssh h2 df") {
		t.Fatalf("%q", first)
	}
	second, _ := r.pre("s1", "Bash", `{"command":"ssh h1"}`)
	if !strings.Contains(second, "sit/prod") || strings.Contains(second, "lib/hosts (loaded") {
		t.Fatalf("the situation again, the library not: %q", second)
	}
	if other, _ := r.pre("s2", "Bash", `{"command":"ssh h1"}`); !strings.Contains(other, "lib/hosts (loaded") {
		t.Fatalf("a new session loads it again: %q", other)
	}
}

func TestKarmaIgnoresBadSignaturesAndRunsWithoutToolRows(t *testing.T) {
	r := newRig(t)
	r.off(config.PieceTools)
	r.kv("sit/a", "bad | x", "sit/a/sig/1", `Bash :: (?=x)`, "sit/b", "good | y", "sit/b/sig/1", `Bash :: ok`)
	got, err := r.pre("s1", "Bash", `{"command":"ok"}`)
	if err != nil || !strings.Contains(got, "sit/b") || strings.Contains(got, "sit/a") {
		t.Fatalf("%q %v", got, err)
	}
	if r.count("PreToolUse") != 0 {
		t.Fatal("tools piece is off: no tool row")
	}
}

func TestEachKnowledgePieceTurnsOff(t *testing.T) {
	setup := func(r *rig) string {
		r.kv("hook/prompt/01", "always", "sit/g", "what | move", "sit/g/words/1", "gate", "sit/g/sig/1", "Bash :: gate")
		r.d.Recall = func(context.Context, string, int) ([]search.KVHit, error) {
			return []search.KVHit{{KV: store.KV{Key: "fact/gate", Value: "v"}, Score: 0.9}}, nil
		}
		tp := r.transcript(userLine, editLine)
		r.stop("s1", tp)
		return tp
	}
	for _, c := range []struct{ piece, marker string }{
		{config.PieceNotes, "hook/prompt/01"},
		{config.PieceCheck, "last turn missed"},
		{config.PieceContext, "context:"},
		{config.PiecePrompt, "fact/gate"},
	} {
		r := newRig(t)
		tp := setup(r)
		if got, _ := r.prompt("s1", "the gate question here", tp); !strings.Contains(got, c.marker) {
			t.Fatalf("%s: %q missing when on: %q", c.piece, c.marker, got)
		}
		r = newRig(t)
		tp = setup(r)
		r.off(c.piece)
		if got, _ := r.prompt("s1", "the gate question here", tp); strings.Contains(got, c.marker) {
			t.Errorf("%s off: %q still in %q", c.piece, c.marker, got)
		}
	}
	r := newRig(t)
	setup(r)
	r.off(config.PiecePrompt)
	if got, _ := r.prompt("s1", "the gate question here", ""); strings.Contains(got, "situation sit/g") {
		t.Errorf("prompt off also silences the words situations: %q", got)
	}
	r = newRig(t)
	setup(r)
	r.off(config.PieceKarma)
	if got, _ := r.pre("s1", "Bash", `{"command":"gate"}`); got != "" {
		t.Errorf("karma off: %q", got)
	}
	r = newRig(t)
	setup(r)
	r.off(config.PieceCheck)
	before := r.count(EventCheck)
	r.stop("s2", r.transcript(userLine, editLine))
	if r.count(EventCheck) != before {
		t.Error("check off: nothing stored")
	}
}

func BenchmarkKarmaNoMatch(b *testing.B) {
	st, err := store.Open(filepath.Join(b.TempDir(), "c.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	set := map[string]string{}
	for i := 0; i < 300; i++ {
		set[fmt.Sprintf("sit/g%d", i)] = "what | move"
		set[fmt.Sprintf("sit/g%d/sig/1", i)] = fmt.Sprintf("Bash :: word%d|other.*thing%d", i, i)
	}
	for i := 0; i < 3000; i++ {
		set[fmt.Sprintf("note/n%d", i)] = "a note"
	}
	if _, err := st.KVWrite(context.Background(), set, nil); err != nil {
		b.Fatal(err)
	}
	d := Deps{Store: st}
	in := Input{SessionID: "s", ToolName: "Bash", ToolInput: []byte(`{"command":"ls -la /tmp && echo hello"}`)}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := d.karma(context.Background(), in); err != nil {
			b.Fatal(err)
		}
	}
}

func TestASlowServerCostsAtMostTheBudget(t *testing.T) {
	old := recallBudget
	recallBudget = 40 * time.Millisecond
	defer func() { recallBudget = old }()
	r := newRig(t)
	r.kv("hook/prompt/01", "always", "sit/g", "what | move", "sit/g/words/1", "gate")
	r.d.Recall = func(ctx context.Context, q string, n int) ([]search.KVHit, error) {
		<-ctx.Done() // an embedder that never answers
		return nil, ctx.Err()
	}
	start := time.Now()
	got, err := r.prompt("s1", "what about the gate today", "")
	if d := time.Since(start); d > time.Second {
		t.Fatalf("waited %v on a search with a 40 ms budget", d)
	}
	if err == nil || !strings.Contains(err.Error(), "prompt recall skipped after") || !strings.Contains(got, "hook/prompt/01: always") || !strings.Contains(got, "situation sit/g") {
		t.Fatalf("%v %q", err, got)
	}
}
