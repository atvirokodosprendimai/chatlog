// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package hook handles Claude Code hook events: it records each one, pushes the transcript into the history as it grows,
// and returns the context to inject. It needs neither the server nor the MCP server: it writes the SQLite file itself.
// The caller never lets an error reach the session: it logs it and exits 0.
package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/chunk"
	"github.com/atvirokodosprendimai/chatlog/internal/config"
	"github.com/atvirokodosprendimai/chatlog/internal/ingest"
	"github.com/atvirokodosprendimai/chatlog/internal/playbook"
	"github.com/atvirokodosprendimai/chatlog/internal/project"
	"github.com/atvirokodosprendimai/chatlog/internal/redact"
	"github.com/atvirokodosprendimai/chatlog/internal/search"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
	"github.com/atvirokodosprendimai/chatlog/internal/tei"
	"github.com/atvirokodosprendimai/chatlog/internal/wake"
)

// Events are the hook events chatlog handles, in the order a session meets them; `chatlog install` wires each one.
var Events = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop", "SubagentStop", "PreCompact", "SessionEnd"}

// ToolEvents are the events that take a tool matcher.
var ToolEvents = []string{"PreToolUse", "PostToolUse"}

// Input is the part of a hook's JSON that is read; fields an event does not send stay empty.
type Input struct {
	Event               string          `json:"hook_event_name"`
	SessionID           string          `json:"session_id"`
	TranscriptPath      string          `json:"transcript_path"`
	AgentTranscriptPath string          `json:"agent_transcript_path"` // SubagentStop: the subagent's own transcript
	Cwd                 string          `json:"cwd"`
	Prompt              string          `json:"prompt"`
	ToolName            string          `json:"tool_name"`
	ToolInput           json.RawMessage `json:"tool_input"`
	ToolResponse        json.RawMessage `json:"tool_response"`
	ToolUseID           string          `json:"tool_use_id"`
	Source              string          `json:"source"`  // SessionStart: startup, resume, clear, compact
	Trigger             string          `json:"trigger"` // PreCompact: manual, auto
	Reason              string          `json:"reason"`  // SessionEnd
}

// Deps is what a handler works with, so a test can give its own store and clock.
type Deps struct {
	Store  *store.Store
	Config config.File
	Now    func() time.Time
	Spool  string // a file for records the database refused (locked past the wait); the next hook writes them; "" drops them
}

// record writes one row; when the database refuses it the row goes to the spool, so a lock held longer than a hook may
// wait costs a delay, not a record.
func (d Deps) record(ctx context.Context, ev store.Event) error {
	_, err := d.Store.AddEvent(ctx, ev)
	if err == nil || d.Spool == "" {
		return err
	}
	b, _ := json.Marshal(ev)
	f, ferr := os.OpenFile(d.Spool, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if ferr != nil {
		return errors.Join(err, ferr)
	}
	defer f.Close()
	// the lock orders this append against a drain reading the file: no line lands in a file already read
	if ferr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); ferr != nil {
		return errors.Join(err, ferr)
	}
	if _, ferr := f.Write(append(b, '\n')); ferr != nil {
		return errors.Join(err, ferr)
	}
	return fmt.Errorf("spooled, to be written by the next hook: %w", err)
}

// drain writes what the spool holds, under the spool's lock, and leaves in it only the rows the database still
// refuses. A spooled PreToolUse whose call was meanwhile recorded by its PostToolUse alone joins that row instead of
// opening a second one.
func (d Deps) drain(ctx context.Context) error {
	if d.Spool == "" {
		return nil
	}
	f, err := os.OpenFile(d.Spool, os.O_RDWR, 0o600)
	if err != nil {
		return nil // nothing spooled
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	b, err := io.ReadAll(f)
	if err != nil || len(b) == 0 {
		return err
	}
	var left []string
	var errs []error
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var ev store.Event
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		var err error
		if ev.Event == "PreToolUse" {
			_, err = d.Store.AdoptPre(ctx, ev)
		} else {
			_, err = d.Store.AddEvent(ctx, ev)
		}
		if err != nil {
			left = append(left, line)
			errs = append(errs, err)
		}
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	if len(left) > 0 {
		if _, err := f.WriteAt([]byte(strings.Join(left, "\n")+"\n"), 0); err != nil {
			return err
		}
	}
	return errors.Join(errs...)
}

// Run reads one event's JSON from r, handles it, and writes the context to inject to w, as Claude Code reads it.
// event is the name given on the command line; empty takes the one in the JSON. Context is written even when the
// recording failed: the returned error is for the log.
func Run(ctx context.Context, event string, r io.Reader, w io.Writer, d Deps) error {
	var in Input
	if err := json.NewDecoder(io.LimitReader(r, 64<<20)).Decode(&in); err != nil {
		return fmt.Errorf("hook input: %w", err)
	}
	if event != "" {
		in.Event = event
	}
	text, err := Handle(ctx, in, d)
	if text != "" {
		out := map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": in.Event, "additionalContext": text}}
		if werr := json.NewEncoder(w).Encode(out); werr != nil {
			err = errors.Join(err, werr)
		}
	}
	return err
}

// Handle records one event and returns the context to inject, "" for none.
func Handle(ctx context.Context, in Input, d Deps) (string, error) {
	now := d.Now()
	h := d.Config.Hooks
	ev := store.Event{Time: now, Event: in.Event, Session: in.SessionID, Cwd: in.Cwd}
	var text string
	errs := []error{d.drain(ctx)}
	switch in.Event {
	case "UserPromptSubmit":
		prev, ok, err := d.Store.LastEvent(ctx, in.SessionID, in.Event)
		errs = append(errs, err)
		if h.On(config.PieceTime) {
			text = clock(now, d.Config.Location(), prev, ok) + " | " + playbook.FirstLine
		}
		ev.Input = Field(in.Prompt, h.Max())
	case "PreToolUse":
		if !h.On(config.PieceTools) {
			return "", errors.Join(errs...)
		}
		ev.Tool, ev.ToolUseID, ev.Input = in.ToolName, in.ToolUseID, inputField(in.ToolInput, h.Max())
		// the call is not in the transcript yet, and this hook must stay the cheapest: no ingest
		return "", errors.Join(append(errs, d.record(ctx, ev))...)
	case "PostToolUse":
		out := outputField(in.ToolResponse, in.ToolInput, h.Max())
		var start time.Time
		ok := false
		if h.On(config.PieceTools) {
			var err error
			start, ok, err = d.Store.FinishTool(ctx, in.ToolUseID, now, out)
			errs = append(errs, err)
			if !ok { // no open PreToolUse row (or the database refused): keep the call anyway, its duration unknown
				ev.Tool, ev.ToolUseID, ev.Input, ev.Output = in.ToolName, in.ToolUseID, inputField(in.ToolInput, h.Max()), out
				errs = append(errs, d.record(ctx, ev))
			}
		}
		if h.On(config.PieceTime) {
			text = "now " + now.UTC().Format(time.RFC3339) + " UTC"
			if ok {
				loc := d.Config.Location()
				text += fmt.Sprintf(" | call started %s %s, took %.3f s", start.In(loc).Format("15:04:05"), zone(start, loc), now.Sub(start).Seconds())
			}
		}
	case "SessionStart":
		ev.Input = in.Source // the digest is made below, after the transcript is pushed, so it holds the latest turn
	case "PreCompact":
		ev.Input = in.Trigger
	case "SessionEnd":
		ev.Input = in.Reason
	case "Stop", "SubagentStop":
	default:
		return "", fmt.Errorf("unknown hook event %q", in.Event)
	}
	if in.Event != "PostToolUse" {
		errs = append(errs, d.record(ctx, ev))
	}
	// The transcript is read once a turn, not at every tool call: re-reading a growing exchange re-indexes its whole text
	// (measured 150 ms for a turn of a few MB), while the tool rows above already hold each call as it happens.
	if h.On(config.PieceIngest) && in.Event != "PostToolUse" {
		since, err := search.ParseSince(d.Config.Since)
		errs = append(errs, err, push(ctx, d.Store, since, in.TranscriptPath), push(ctx, d.Store, since, in.AgentTranscriptPath))
	}
	if in.Event == "SessionStart" && h.On(config.PieceRecall) {
		digest, err := wake.Digest(ctx, d.Store, wake.KindOf(in.Source), project.Of(in.Cwd), now, h.Wake())
		if err != nil { // the rule still goes in: a session must never start without it
			digest = playbook.Rule
		}
		text = digest
		errs = append(errs, err)
	}
	return text, errors.Join(errs...)
}

// push reads what a transcript gained since its cursor into the history: the same idempotent step the server runs.
// pushMax is the most a hook reads of one transcript: about one turn. A larger backlog (a transcript older than the
// install, a turn with huge tool output) is left to the server's next scan or `chatlog ingest --all`, so no hook waits on it.
const pushMax = 8 << 20

func push(ctx context.Context, st *store.Store, since time.Time, path string) error {
	if path == "" {
		return nil
	}
	in := &ingest.Ingester{Store: st, Limit: chunk.DefaultLimit, MaxBehind: pushMax, Since: since}
	err := in.File(ctx, path)
	switch {
	case err == nil || errors.Is(err, fs.ErrNotExist):
		return nil
	case errors.Is(err, ingest.ErrBehind):
		return fmt.Errorf("%s has over %d MB not yet read: left to the server or `chatlog ingest --all`", path, pushMax>>20)
	}
	return fmt.Errorf("ingest %s: %w", path, err)
}

// clock is the prompt's timestamp: UTC, the local time with its zone, and the time since the session's previous prompt.
func clock(now time.Time, loc *time.Location, prev time.Time, hasPrev bool) string {
	s := fmt.Sprintf("now %s UTC | %s %s", now.UTC().Format(time.RFC3339), now.In(loc).Format("2006-01-02T15:04:05-07:00"), zone(now, loc))
	if hasPrev {
		return s + " | previous prompt " + age(now.Sub(prev)) + " ago"
	}
	return s + " | first prompt of this session"
}

// zone names a location for a reader: its IANA name when it has one, else its abbreviation.
func zone(t time.Time, loc *time.Location) string {
	if n := loc.String(); n != "Local" && n != "" {
		return n
	}
	abbr, _ := t.In(loc).Zone()
	return abbr
}

// age rounds a duration to what a person reads: seconds under an hour, minutes above.
func age(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Hour {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Minute).String()
}

// Field is a stored text: secrets redacted by shape first, then capped at max bytes on a rune boundary, with a marker
// saying how much was left out. Redacting before the cut means a secret is never stored half, past the redactor's reach.
func Field(s string, max int) string {
	s = redact.String(s)
	if len(s) <= max {
		return s
	}
	kept := tei.Clip(s, max)
	return kept + fmt.Sprintf("…[+%d bytes]", len(s)-len(kept))
}

// inputField is a tool's input as stored. When the call touches a dotenv file only the file names are kept, not even
// the command: such a file is all values, and value shapes are not enough to find every secret in it.
func inputField(input json.RawMessage, max int) string {
	if touchesEnv(input) {
		return Field(envNames(input), max)
	}
	return Field(text(input), max)
}

// outputField is a tool's output as stored; nothing of it when the input touches a dotenv file.
func outputField(output, input json.RawMessage, max int) string {
	if touchesEnv(input) {
		return "[a dotenv file: content not recorded]"
	}
	return Field(text(output), max)
}

// touchesEnv says whether any string in a tool's input names a dotenv file.
func touchesEnv(input json.RawMessage) bool {
	found := false
	walkStrings(input, func(s string) {
		if len(redact.DotenvNames(s)) > 0 {
			found = true
		}
	})
	return found
}

// envNames is the dotenv file names in the input, so the call stays findable by its file.
func envNames(input json.RawMessage) string {
	var keep []string
	walkStrings(input, func(s string) {
		for _, m := range redact.DotenvNames(s) {
			keep = append(keep, m)
		}
	})
	return "[touches " + strings.Join(keep, " ") + ": content not recorded]"
}

func walkStrings(raw json.RawMessage, f func(string)) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			f(x)
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
}

// text is a JSON value as a person reads it: a string's own text, anything else compact JSON.
func text(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}

// LogPath is the file a failing hook writes to, in the data dir.
func LogPath(dataDir string) string { return filepath.Join(dataDir, "hooks.log") }

// Rotate moves a log over 1 MB aside to <path>.1, replacing the one before: a log never takes more than about 2 MB.
func Rotate(p string) {
	if fi, err := os.Stat(p); err == nil && fi.Size() > 1<<20 {
		os.Rename(p, p+".1")
	}
}

// Log appends one line to the hook log, keeping it under about 1 MB by moving a full log aside once.
func Log(dataDir string, now time.Time, format string, args ...any) {
	p := LogPath(dataDir)
	Rotate(p)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", now.UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}
