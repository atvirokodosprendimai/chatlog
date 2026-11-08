// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package transcript turns Claude Code transcript files into exchanges: M's message, what the assistant did, its answer.
//
// f(lines of a file, offset) -> exchanges + cursor. It holds no state between calls: the cursor stands at the line that
// opens the last exchange, so a later read rebuilds that exchange whole from the file.
package transcript

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/redact"
)

// PartKind says what a step inside an exchange is.
type PartKind int

// The kinds of a Part. Only ToolOutput, Machine and ToolCall may ever be cut from the embedded text.
const (
	Text PartKind = iota
	ToolCall
	ToolOutput
	Machine
)

// Part is one step between the user's words and the answer. A ToolCall carries when it started and, once its result
// came, how long it took and whether the tool reported an error; its ToolOutput carries the same Error.
type Part struct {
	Kind  PartKind
	Name  string // the tool's name for a ToolCall
	Text  string
	ID    string        // the tool_use id, joining a call to its result
	Time  time.Time     // a ToolCall's start
	Took  time.Duration // a ToolCall's duration: its result's time minus its start; 0 while no result came
	Error bool          // the tool's result was an error
}

// Exchange is one message of M's and everything up to his next one. All text is already redacted.
type Exchange struct {
	UUID    string // of the row holding M's message: stable across re-reads and across a resumed copy
	Session string
	Agent   string // set when M typed into a subagent's window
	Cwd     string
	Source  string
	Offset  int64 // where the opening line starts in the file
	Time    time.Time
	End     time.Time
	Words   string
	Answer  string // the last assistant text
	Parts   []Part
	Closed  bool // false while no later message of M's has been seen
}

// Tail is what a read leaves behind.
type Tail struct {
	ReadTo int64     // end of the last complete line
	Cursor int64     // where the next read must start: the opening line of Open, or ReadTo
	Open   *Exchange // the exchange not yet closed by a later message
}

type origin struct {
	Kind string `json:"kind"`
}

type row struct {
	Type            string      `json:"type"`
	UUID            string      `json:"uuid"`
	SessionID       string      `json:"sessionId"`
	AgentID         string      `json:"agentId"`
	Cwd             string      `json:"cwd"`
	Timestamp       string      `json:"timestamp"`
	IsMeta          bool        `json:"isMeta"`
	IsSidechain     bool        `json:"isSidechain"`
	IsCompact       bool        `json:"isCompactSummary"`
	TranscriptOnly  bool        `json:"isVisibleInTranscriptOnly"`
	IsAPIError      bool        `json:"isApiErrorMessage"`
	ScheduledTaskID string      `json:"scheduledTaskId"`
	Origin          *origin     `json:"origin"`
	Message         *message    `json:"message"`
	Attachment      *attachment `json:"attachment"`
}

type message struct {
	Content json.RawMessage `json:"content"`
}

type attachment struct {
	Type        string          `json:"type"`
	Prompt      json.RawMessage `json:"prompt"`
	Origin      *origin         `json:"origin"`
	CommandMode string          `json:"commandMode"`
	IsMeta      bool            `json:"isMeta"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`          // tool_use
	ToolUseID string          `json:"tool_use_id"` // tool_result
	IsError   bool            `json:"is_error"`    // tool_result
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
}

// machinePrefixes are messages the harness puts in M's role. Older transcripts carry no origin, so the text is all there is.
var machinePrefixes = []string{
	"<task-notification", "<agent-message", "<local-command", "<bash-stdout", "<bash-stderr",
	"[Request interrupted", "Another Claude session", "Stop hook feedback", "Base directory for this skill",
	"You are (re)starting", "(Re-invocation of", "Memory checkpoint", "The coordinator sent a message",
	"This session is being continued", "Caveat:",
}

var (
	reminderRE = regexp.MustCompile(`(?s)<system-reminder[^>]*>.*?</system-reminder>`)
	cmdNameRE  = regexp.MustCompile(`(?s)<command-name>(.*?)</command-name>`)
	cmdArgsRE  = regexp.MustCompile(`(?s)<command-args>(.*?)</command-args>`)
)

func clean(s string) string { return strings.TrimSpace(reminderRE.ReplaceAllString(s, "")) }

func isMachineText(s string) bool {
	for _, p := range machinePrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// humanText rewrites a slash command to what M typed.
func humanText(s string) string {
	if !strings.HasPrefix(s, "<command-") {
		return s
	}
	name := cmdNameRE.FindStringSubmatch(s)
	if name == nil {
		return s
	}
	out := strings.TrimSpace(name[1])
	if args := cmdArgsRE.FindStringSubmatch(s); args != nil && strings.TrimSpace(args[1]) != "" {
		out += " " + strings.TrimSpace(args[1])
	}
	return out
}

// contentText reads a content field that is a string or a list of blocks.
func contentText(raw json.RawMessage) (string, []block) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if raw[0] == '"' {
		if json.Unmarshal(raw, &s) != nil {
			return "", nil
		}
		return s, nil
	}
	var blocks []block
	if json.Unmarshal(raw, &blocks) != nil {
		return "", nil
	}
	var b strings.Builder
	for _, bl := range blocks {
		if bl.Type == "text" {
			b.WriteString(bl.Text)
		}
	}
	return b.String(), blocks
}

// toolInput shows a command as itself and anything else as its JSON.
func toolInput(raw json.RawMessage) string {
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(raw, &in) == nil && in.Command != "" {
		return in.Command
	}
	return string(raw)
}

// toolOutput flattens a tool_result's content; an image is named, not kept.
func toolOutput(raw json.RawMessage) string {
	text, blocks := contentText(raw)
	for _, b := range blocks {
		if b.Type == "image" {
			text += "[image]"
		}
	}
	return clean(text)
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// builder holds the exchange being read.
type builder struct {
	source string
	isSub  bool
	cur    *Exchange
	seen   map[string]bool // assistant uuids in this read: a resume may repeat them
	env    map[string]bool // tool_use ids whose input names a dotenv file: their output is not kept
}

// finish moves the last text to Answer.
func finish(e Exchange) Exchange {
	for i := len(e.Parts) - 1; i >= 0; i-- {
		if e.Parts[i].Kind == Text {
			e.Answer = e.Parts[i].Text
			e.Parts = append(e.Parts[:i:i], e.Parts[i+1:]...)
			break
		}
	}
	return e
}

// open starts an exchange from M's words. It returns the exchange it closes, if any.
func (b *builder) open(r row, words string, offset int64) *Exchange {
	if b.cur != nil && b.cur.Words == words && len(b.cur.Parts) == 0 {
		return nil // the queued copy of a message and its own row, side by side
	}
	var closed *Exchange
	if b.cur != nil {
		e := finish(*b.cur)
		e.Closed = true
		closed = &e
	}
	t := parseTime(r.Timestamp)
	b.cur = &Exchange{UUID: r.UUID, Session: r.SessionID, Cwd: r.Cwd, Source: b.source, Offset: offset, Time: t, End: t, Words: words}
	if b.isSub {
		b.cur.Agent = r.AgentID
		if b.cur.Agent == "" {
			b.cur.Agent = strings.TrimSuffix(strings.TrimPrefix(filepath.Base(b.source), "agent-"), ".jsonl")
		}
	}
	if b.cur.Session == "" {
		b.cur.Session = strings.TrimSuffix(filepath.Base(b.source), ".jsonl")
	}
	return closed
}

func (b *builder) add(r row, p Part) {
	if b.cur == nil {
		return
	}
	p.Text = redact.String(p.Text)
	b.cur.Parts = append(b.cur.Parts, p)
	if t := parseTime(r.Timestamp); !t.IsZero() {
		b.cur.End = t
	}
	if b.cur.Cwd == "" {
		b.cur.Cwd = r.Cwd
	}
}

// result closes the call a tool_result answers: its duration and whether it failed.
func (b *builder) result(id string, at time.Time, isError bool) {
	if b.cur == nil || id == "" {
		return
	}
	for i := len(b.cur.Parts) - 1; i >= 0; i-- {
		p := &b.cur.Parts[i]
		if p.Kind == ToolCall && p.ID == id {
			if !p.Time.IsZero() && !at.IsZero() && at.After(p.Time) {
				p.Took = at.Sub(p.Time)
			}
			p.Error = isError
			return
		}
	}
}

// feed takes one row and returns an exchange it closed.
func (b *builder) feed(r row, offset int64) *Exchange {
	if r.IsSidechain && !b.isSub {
		return nil
	}
	switch r.Type {
	case "assistant":
		if r.Message == nil || r.IsAPIError || b.seen[r.UUID] {
			return nil
		}
		b.seen[r.UUID] = true
		_, blocks := contentText(r.Message.Content)
		for _, bl := range blocks {
			switch bl.Type {
			case "text":
				if t := strings.TrimSpace(bl.Text); t != "" {
					b.add(r, Part{Kind: Text, Text: t})
				}
			case "tool_use":
				p := Part{Kind: ToolCall, Name: bl.Name, Text: toolInput(bl.Input), ID: bl.ID, Time: parseTime(r.Timestamp)}
				// a dotenv file is all values: keep only its name, here and in the call's output
				if names := redact.DotenvNames(p.Text); len(names) > 0 {
					p.Text = "[touches " + strings.Join(names, " ") + ": content not recorded]"
					b.env[bl.ID] = true
				}
				b.add(r, p)
			}
		}
	case "user":
		return b.feedUser(r, offset)
	case "attachment":
		a := r.Attachment
		if a == nil || a.Type != "queued_command" {
			return nil
		}
		// Measured: origin human is M, also in a subagent's window; coordinator and peer are agents, even when they quote him.
		human := a.Origin != nil && a.Origin.Kind == "human"
		if a.Origin == nil && a.CommandMode == "prompt" && !a.IsMeta {
			human = true
		}
		text, _ := contentText(a.Prompt)
		text = clean(text)
		if text == "" {
			return nil
		}
		if !human || isMachineText(text) {
			b.add(r, Part{Kind: Machine, Text: text})
			return nil
		}
		return b.open(r, redact.String(humanText(text)), offset)
	}
	return nil
}

func (b *builder) feedUser(r row, offset int64) *Exchange {
	if r.Message == nil {
		return nil
	}
	text, blocks := contentText(r.Message.Content)
	for _, bl := range blocks {
		if bl.Type == "tool_result" {
			b.result(bl.ToolUseID, parseTime(r.Timestamp), bl.IsError)
			out := toolOutput(bl.Content)
			if b.env[bl.ToolUseID] {
				out = "[a dotenv file: content not recorded]"
			}
			b.add(r, Part{Kind: ToolOutput, Text: out, ID: bl.ToolUseID, Error: bl.IsError})
		}
	}
	text = clean(text)
	if text == "" {
		return nil
	}
	if r.IsMeta || r.IsCompact || r.TranscriptOnly {
		return nil
	}
	machine := r.ScheduledTaskID != "" || isMachineText(text)
	if r.Origin != nil && r.Origin.Kind != "human" {
		machine = true
	}
	if r.Origin == nil && r.IsSidechain {
		machine = true // a subagent's brief: written by the agent that started it
	}
	if machine {
		if !strings.HasPrefix(text, "[Request interrupted") {
			b.add(r, Part{Kind: Machine, Text: text})
		}
		return nil
	}
	return b.open(r, redact.String(humanText(text)), offset)
}

// Read reads path from offset up to its last complete line and calls emit for each closed exchange, with the offset a later
// read may start from. A line still being written is left for the next read; a complete line that is not JSON is passed over.
func Read(path string, offset int64, emit func(e Exchange, next int64) error) (Tail, error) {
	f, err := os.Open(path)
	if err != nil {
		return Tail{}, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return Tail{}, err
	}
	b := &builder{source: path, isSub: filepath.Base(filepath.Dir(path)) == "subagents", seen: map[string]bool{}, env: map[string]bool{}}
	br := bufio.NewReaderSize(f, 1<<20)
	pos := offset
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return Tail{}, fmt.Errorf("%s: %w", path, err)
		}
		var r row
		if json.Unmarshal(line, &r) == nil {
			if closed := b.feed(r, pos); closed != nil {
				if err := emit(*closed, b.cur.Offset); err != nil {
					return Tail{}, err
				}
			}
		}
		pos += int64(len(line))
	}
	t := Tail{ReadTo: pos, Cursor: pos}
	if b.cur != nil {
		e := finish(*b.cur)
		t.Open = &e
		t.Cursor = e.Offset
	}
	return t, nil
}
