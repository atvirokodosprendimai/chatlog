// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package hook

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // the zone assertions below must not depend on the machine having tzdata
	"unicode/utf8"

	"github.com/atvirokodosprendimai/chatlog/internal/config"
	"github.com/atvirokodosprendimai/chatlog/internal/playbook"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

type rig struct {
	t   *testing.T
	d   Deps
	now time.Time
	dir string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := &rig{t: t, now: time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC), dir: dir}
	vilnius, _ := time.LoadLocation("Europe/Vilnius")
	r.d = Deps{Store: st, Config: config.File{Timezone: "Europe/Vilnius"}, Now: func() time.Time { return r.now }}
	if vilnius == nil {
		r.d.Config.Timezone = ""
	}
	return r
}

// run sends one event as Claude Code would and returns the injected context ("" when nothing was printed).
func (r *rig) run(event, js string) (string, error) {
	r.t.Helper()
	var out bytes.Buffer
	err := Run(context.Background(), event, strings.NewReader(js), &out, r.d)
	if out.Len() == 0 {
		return "", err
	}
	var got struct {
		H struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if jerr := json.Unmarshal(out.Bytes(), &got); jerr != nil {
		r.t.Fatalf("output is not the JSON Claude Code reads: %q", out.String())
	}
	if got.H.Event != event {
		r.t.Fatalf("hookEventName %q, want %q", got.H.Event, event)
	}
	return got.H.Context, err
}

func (r *rig) events() []store.Event {
	r.t.Helper()
	evs, err := r.d.Store.Events(context.Background(), "", 100)
	if err != nil {
		r.t.Fatal(err)
	}
	return evs
}

func TestPromptStampsTimeAndAge(t *testing.T) {
	r := newRig(t)
	ctx, err := r.run("UserPromptSubmit", `{"session_id":"s1","cwd":"/u/webshop","prompt":"deploy with API_KEY=hunter2abc please"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ctx, "now 2026-11-08T09:00:00Z UTC") || !strings.Contains(ctx, "first prompt of this session") {
		t.Fatalf("context %q", ctx)
	}
	if r.d.Config.Timezone != "" && !strings.Contains(ctx, "2026-11-08T11:00:00+02:00 Europe/Vilnius") {
		t.Fatalf("local time missing: %q", ctx)
	}
	r.now = r.now.Add(90 * time.Second)
	ctx, _ = r.run("UserPromptSubmit", `{"session_id":"s1","prompt":"next"}`)
	if !strings.Contains(ctx, "previous prompt 1m30s ago") {
		t.Fatalf("age missing: %q", ctx)
	}
	ctx, _ = r.run("UserPromptSubmit", `{"session_id":"other","prompt":"x"}`)
	if !strings.Contains(ctx, "first prompt") {
		t.Fatalf("age crossed sessions: %q", ctx)
	}
	evs := r.events()
	if len(evs) != 3 || evs[0].Event != "UserPromptSubmit" || evs[0].Session != "s1" || evs[0].Cwd != "/u/webshop" {
		t.Fatalf("rows %+v", evs)
	}
	if strings.Contains(evs[0].Input, "hunter2abc") || !strings.Contains(evs[0].Input, "deploy with API_KEY=") {
		t.Fatalf("prompt not redacted or lost: %q", evs[0].Input)
	}
}

func TestToolCallTimedAcrossPreAndPost(t *testing.T) {
	r := newRig(t)
	if ctx, err := r.run("PreToolUse", `{"session_id":"s1","tool_name":"Bash","tool_use_id":"tu1","tool_input":{"command":"curl -H 'Authorization: Bearer abcdef123456' x"}}`); err != nil || ctx != "" {
		t.Fatalf("pre injects nothing: %q %v", ctx, err)
	}
	r.now = r.now.Add(2500 * time.Millisecond)
	ctx, err := r.run("PostToolUse", `{"session_id":"s1","tool_name":"Bash","tool_use_id":"tu1","tool_input":{"command":"x"},"tool_response":{"stdout":"done"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ctx, "took 2.500 s") || !strings.Contains(ctx, "call started ") {
		t.Fatalf("context %q", ctx)
	}
	if r.d.Config.Timezone != "" && !strings.Contains(ctx, "call started 11:00:00 Europe/Vilnius") {
		t.Fatalf("start time not local: %q", ctx)
	}
	evs := r.events()
	if len(evs) != 1 {
		t.Fatalf("one row per call, got %+v", evs)
	}
	e := evs[0]
	if e.Tool != "Bash" || e.ToolUseID != "tu1" || e.DurationMs == nil || *e.DurationMs != 2500 || e.Output != `{"stdout":"done"}` {
		t.Fatalf("row %+v", e)
	}
	if strings.Contains(e.Input, "abcdef123456") {
		t.Fatalf("authorization kept: %q", e.Input)
	}
}

func TestPostWithoutPreKeepsTheCall(t *testing.T) {
	r := newRig(t)
	ctx, err := r.run("PostToolUse", `{"session_id":"s1","tool_name":"Read","tool_use_id":"tu9","tool_input":{"file_path":"/a"},"tool_response":"text"}`)
	if err != nil || strings.Contains(ctx, "took") || !strings.HasPrefix(ctx, "now ") {
		t.Fatalf("%q %v", ctx, err)
	}
	evs := r.events()
	if len(evs) != 1 || evs[0].DurationMs != nil || evs[0].Output != "text" || evs[0].Tool != "Read" {
		t.Fatalf("%+v", evs)
	}
}

func TestFieldsAreCapped(t *testing.T) {
	r := newRig(t)
	r.d.Config.Hooks.MaxFieldBytes = 100
	big := strings.Repeat("ą", 500) // 1000 bytes, two per rune
	r.run("PreToolUse", fmt.Sprintf(`{"session_id":"s","tool_name":"Write","tool_use_id":"t","tool_input":{"content":%q}}`, big))
	r.run("PostToolUse", fmt.Sprintf(`{"session_id":"s","tool_name":"Write","tool_use_id":"t","tool_input":{},"tool_response":%q}`, big))
	e := r.events()[0]
	for name, v := range map[string]string{"input": e.Input, "output": e.Output} {
		kept, _, ok := strings.Cut(v, "…[+")
		if !ok || len(kept) > 100 || len(kept) < 98 || !strings.HasSuffix(v, " bytes]") { // 98: a cut on a rune boundary loses at most one 2-byte rune
			t.Fatalf("%s not capped at 100 with a marker: %d bytes %q", name, len(v), v)
		}
		if !utf8.ValidString(kept) {
			t.Fatalf("%s cut inside a rune", name)
		}
	}
}

func TestDotenvContentNotRecorded(t *testing.T) {
	r := newRig(t)
	r.run("PreToolUse", `{"session_id":"s","tool_name":"Read","tool_use_id":"t1","tool_input":{"file_path":"/app/.env"}}`)
	r.run("PostToolUse", `{"session_id":"s","tool_name":"Read","tool_use_id":"t1","tool_input":{"file_path":"/app/.env"},"tool_response":{"content":"DB_PASS=plainvalue9"}}`)
	r.run("PreToolUse", `{"session_id":"s","tool_name":"Bash","tool_use_id":"t2","tool_input":{"command":"cat .env.local"}}`)
	r.run("PostToolUse", `{"session_id":"s","tool_name":"Bash","tool_use_id":"t2","tool_input":{"command":"cat .env.local"},"tool_response":{"stdout":"X=othervalue7"}}`)
	r.run("PreToolUse", `{"session_id":"s","tool_name":"Write","tool_use_id":"t3","tool_input":{"file_path":"/app/.env","content":"NEW=thirdvalue5\n"}}`)
	for _, e := range r.events() {
		for _, secret := range []string{"plainvalue9", "othervalue7", "thirdvalue5"} {
			if strings.Contains(e.Input+e.Output, secret) {
				t.Fatalf("dotenv value stored: %+v", e)
			}
		}
		if !strings.Contains(e.Input, ".env") {
			t.Fatalf("the file named should stay findable: %+v", e)
		}
	}
	// dotenv files by other names, and a command that writes one: none of their values may be stored
	for i, cmd := range []string{
		"cat config/prod.env", "cat secrets.env", "cat .envrc", "grep KEY <.env", "cp a.env,b", "x:.env.local",
		"echo 'DB_PASS=hunter2' >> .env", "printf 'TOKEN_X=zzzval42\\n' > app/.env",
		"cat .env*", "cp .env{,.bak} /tmp", "ls .env/", "less .ENV", "cat config/Prod.Env",
	} {
		id := fmt.Sprintf("e%d", i)
		r.run("PreToolUse", fmt.Sprintf(`{"session_id":"s","tool_name":"Bash","tool_use_id":%q,"tool_input":{"command":%q}}`, id, cmd))
		r.run("PostToolUse", fmt.Sprintf(`{"session_id":"s","tool_name":"Bash","tool_use_id":%q,"tool_input":{"command":%q},"tool_response":{"stdout":"S3CRET=leakvalue"}}`, id, cmd))
		e := r.events()[len(r.events())-1]
		if strings.Contains(e.Input+e.Output, "leakvalue") || strings.Contains(e.Input, "hunter2") || strings.Contains(e.Input, "zzzval42") {
			t.Fatalf("%q stored a value: %+v", cmd, e)
		}
	}
	// a file merely named like one is not a dotenv file
	r.run("PreToolUse", `{"session_id":"s","tool_name":"Read","tool_use_id":"t4","tool_input":{"file_path":"/app/environment.go"}}`)
	r.run("PostToolUse", `{"session_id":"s","tool_name":"Read","tool_use_id":"t4","tool_input":{"file_path":"/app/environment.go"},"tool_response":"package app"}`)
	if evs := r.events(); evs[len(evs)-1].Output != "package app" {
		t.Fatalf("ordinary file hidden: %+v", evs[len(evs)-1])
	}
}

func transcript(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "s1.jsonl")
	lines := `{"type":"user","uuid":"u1","sessionId":"s1","cwd":"/u/webshop","timestamp":"2026-11-08T09:00:00Z","origin":{"kind":"human"},"message":{"content":"what changed"}}` + "\n" +
		`{"type":"assistant","uuid":"a1","sessionId":"s1","timestamp":"2026-11-08T09:00:05Z","message":{"content":[{"type":"text","text":"two files"}]}}` + "\n"
	if err := os.WriteFile(p, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTurnEndPushesTheTranscript(t *testing.T) {
	r := newRig(t)
	p := transcript(t, r.dir)
	js := fmt.Sprintf(`{"session_id":"s1","transcript_path":%q}`, p)
	if _, err := r.run("PostToolUse", strings.Replace(js, "}", `,"tool_use_id":"x"}`, 1)); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.d.Store.Stats(context.Background()); st.Chunks != 0 {
		t.Fatalf("a tool call must not read the transcript: %+v", st)
	}
	for _, ev := range []string{"Stop", "Stop"} { // twice: the second read finds nothing new and doubles nothing
		if _, err := r.run(ev, js); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := r.d.Store.Stats(context.Background())
	if st.Chunks != 1 || st.Projects[0].Project != "webshop" {
		t.Fatalf("exchange not pushed once: %+v", st)
	}
	row, err := r.d.Store.Get(context.Background(), 1)
	if err != nil || row.Words != "what changed" || row.Answer != "two files" {
		t.Fatalf("%+v %v", row, err)
	}
}

func TestBacklogIsLeftToTheServer(t *testing.T) {
	r := newRig(t)
	p := transcript(t, r.dir)
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	f.Write(bytes.Repeat([]byte(`{"type":"progress"}`+"\n"), pushMax/19+1))
	f.Close()
	_, err := r.run("Stop", fmt.Sprintf(`{"session_id":"s1","transcript_path":%q}`, p))
	if err == nil || !strings.Contains(err.Error(), "chatlog ingest --all") {
		t.Fatalf("a backlog is said in the log, with what to run: %v", err)
	}
	if st, _ := r.d.Store.Stats(context.Background()); st.Chunks != 0 {
		t.Fatalf("a hook read more than one turn: %+v", st)
	}
	if evs := r.events(); len(evs) != 1 {
		t.Fatalf("the Stop row is kept: %+v", evs)
	}
}

func TestHookIngestHonoursSince(t *testing.T) {
	r := newRig(t)
	r.d.Config.Since = "2026-11-09" // the transcript's exchange is from 2026-11-08
	r.run("Stop", fmt.Sprintf(`{"session_id":"s1","transcript_path":%q}`, transcript(t, r.dir)))
	if st, _ := r.d.Store.Stats(context.Background()); st.Chunks != 0 {
		t.Fatalf("an exchange older than since was indexed: %+v", st)
	}
}

func TestEmptyToolUseIDClosesNothing(t *testing.T) {
	r := newRig(t)
	r.run("PreToolUse", `{"session_id":"s","tool_name":"A","tool_use_id":"","tool_input":{}}`)
	r.run("PostToolUse", `{"session_id":"s","tool_name":"B","tool_use_id":"","tool_input":{},"tool_response":"b"}`)
	evs := r.events()
	if len(evs) != 2 || evs[0].DurationMs != nil || evs[0].Output != "" || evs[1].Tool != "B" {
		t.Fatalf("a call without an id closed another's row: %+v", evs)
	}
}

func TestLockedDatabaseSpoolsInsteadOfDropping(t *testing.T) {
	r := newRig(t)
	st, err := store.OpenWith(filepath.Join(r.dir, "c.db"), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r.d.Store, r.d.Spool = st, filepath.Join(r.dir, "spool.jsonl")
	lock, err := sql.Open("sqlite", "file:"+filepath.Join(r.dir, "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	tx, err := lock.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES('lock', 'held')`); err != nil { // takes the write lock
		t.Fatal(err)
	}
	if _, err := r.run("PreToolUse", `{"session_id":"s","tool_name":"Bash","tool_use_id":"t1","tool_input":{"command":"ls"}}`); err == nil || !strings.Contains(err.Error(), "spooled") {
		t.Fatalf("a locked database must spool the row: %v", err)
	}
	tx.Rollback()
	r.now = r.now.Add(time.Second)
	if _, err := r.run("PostToolUse", `{"session_id":"s","tool_name":"Bash","tool_use_id":"t1","tool_input":{},"tool_response":"ok"}`); err != nil {
		t.Fatal(err)
	}
	evs := r.events()
	if len(evs) != 1 || evs[0].DurationMs == nil || *evs[0].DurationMs != 1000 {
		t.Fatalf("the spooled row was not written before its call closed: %+v", evs)
	}
	if fi, err := os.Stat(r.d.Spool); err == nil && fi.Size() != 0 {
		t.Fatalf("spool not emptied: %d bytes", fi.Size())
	}
}

// A PreToolUse spooled while its PostToolUse found no row to close: the drain joins the two into one row.
func TestSpooledPreJoinsItsPost(t *testing.T) {
	r := newRig(t)
	r.d.Spool = filepath.Join(r.dir, "spool.jsonl")
	pre := store.Event{Time: r.now, Event: "PreToolUse", Session: "s", Tool: "Bash", ToolUseID: "t9", Input: "make"}
	b, _ := json.Marshal(pre)
	os.WriteFile(r.d.Spool, append(b, '\n'), 0o600)
	r.now = r.now.Add(3 * time.Second)
	if _, err := r.d.Store.AddEvent(context.Background(), store.Event{Time: r.now, Event: "PostToolUse", Session: "s", Tool: "Bash", ToolUseID: "t9", Output: "ok"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.run("Stop", `{"session_id":"s"}`); err != nil {
		t.Fatal(err)
	}
	var calls []store.Event
	for _, e := range r.events() {
		if e.ToolUseID == "t9" {
			calls = append(calls, e)
		}
	}
	if len(calls) != 1 || calls[0].Event != "PreToolUse" || calls[0].Input != "make" || calls[0].Output != "ok" || calls[0].DurationMs == nil || *calls[0].DurationMs != 3000 {
		t.Fatalf("one call must be one row: %+v", calls)
	}
}

func TestSessionStartPointsAtRecall(t *testing.T) {
	r := newRig(t)
	ctx, _ := r.run("SessionStart", `{"session_id":"s1","cwd":"/u/webshop","source":"startup"}`)
	if !strings.HasPrefix(ctx, playbook.FirstLine) || !strings.Contains(ctx, `chat_tail project="webshop"`) || strings.Contains(ctx, "COMPACTED") {
		t.Fatalf("%q", ctx)
	}
	ctx, _ = r.run("SessionStart", `{"session_id":"s1","cwd":"/u/webshop","source":"compact"}`)
	if !strings.Contains(ctx, "THE CONVERSATION WAS COMPACTED") || len(ctx) > config.DefaultWakeBytes {
		t.Fatalf("%q", ctx)
	}
	for _, ev := range []string{"PreCompact", "SessionEnd", "SubagentStop", "Stop"} {
		if ctx, err := r.run(ev, `{"session_id":"s1","trigger":"auto","reason":"exit"}`); err != nil || ctx != "" {
			t.Fatalf("%s: %q %v", ev, ctx, err)
		}
	}
	got := []string{}
	for _, e := range r.events() {
		got = append(got, e.Event+":"+e.Input)
	}
	want := "SessionStart:startup SessionStart:compact PreCompact:auto SessionEnd:exit SubagentStop: Stop:"
	if strings.Join(got, " ") != want {
		t.Fatalf("boundaries %q, want %q", strings.Join(got, " "), want)
	}
}

func TestPiecesTurnOff(t *testing.T) {
	r := newRig(t)
	r.d.Config.Hooks.Off = []string{"time", "tools", "recall", "ingest"}
	p := transcript(t, r.dir)
	for ev, js := range map[string]string{
		"UserPromptSubmit": `{"session_id":"s","prompt":"x"}`,
		"PreToolUse":       `{"session_id":"s","tool_use_id":"t","tool_name":"Bash"}`,
		"PostToolUse":      `{"session_id":"s","tool_use_id":"t","tool_name":"Bash"}`,
		"SessionStart":     `{"session_id":"s","source":"startup"}`,
		"Stop":             fmt.Sprintf(`{"session_id":"s","transcript_path":%q}`, p),
	} {
		if ctx, err := r.run(ev, js); ctx != "" || err != nil {
			t.Fatalf("%s injected %q (%v) with every piece off", ev, ctx, err)
		}
	}
	for _, e := range r.events() {
		if e.Tool != "" {
			t.Fatalf("tool row with tools off: %+v", e)
		}
	}
	if st, _ := r.d.Store.Stats(context.Background()); st.Chunks != 0 {
		t.Fatalf("ingested with ingest off")
	}
}

func TestBadInputIsAnErrorAndPrintsNothing(t *testing.T) {
	r := newRig(t)
	var out bytes.Buffer
	if err := Run(context.Background(), "Stop", strings.NewReader("not json"), &out, r.d); err == nil || out.Len() != 0 {
		t.Fatalf("%v %q", err, out.String())
	}
	if err := Run(context.Background(), "Nonsense", strings.NewReader("{}"), &out, r.d); err == nil || out.Len() != 0 {
		t.Fatalf("unknown event: %v %q", err, out.String())
	}
	if _, err := r.run("", `{"hook_event_name":"Stop","session_id":"s"}`); err != nil {
		t.Fatalf("event from the JSON when none is given: %v", err)
	}
}
