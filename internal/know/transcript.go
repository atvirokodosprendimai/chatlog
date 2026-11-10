// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package know

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
)

// TailBytes is the most of a transcript's end that is read: a turn and its last usage fit in it, a whole long session
// does not have to. chunkBytes is how much is read at a time, from the end: a turn is short, so most reads stop at the first.
const (
	TailBytes  = 4 << 20
	chunkBytes = 128 << 10
)

// Use is one tool call of a turn.
type Use struct {
	Name  string
	Input json.RawMessage
}

// Turn is the last turn of a transcript: the prompt the user typed and the tool calls the assistant made after it, in order.
type Turn struct {
	Prompt string
	Uses   []Use
}

type entry struct {
	Type      string `json:"type"`
	Sidechain bool   `json:"isSidechain"`
	Meta      bool   `json:"isMeta"`
	Message   struct {
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			In          int `json:"input_tokens"`
			CacheRead   int `json:"cache_read_input_tokens"`
			CacheCreate int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// backwards calls visit with the lines of a file, last first, until it returns true; it reads chunkBytes at a time from
// the end and never more than TailBytes. A line cut by the start of that tail is not visited.
func backwards(path string, visit func(line []byte) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	pos, limit := fi.Size(), max(fi.Size()-TailBytes, 0)
	var rest []byte // the start of what was read last: a line whose beginning is still unread
	for pos > limit {
		n := min(int64(chunkBytes), pos-limit)
		pos -= n
		buf := make([]byte, n, int(n)+len(rest))
		if _, err := f.ReadAt(buf, pos); err != nil {
			return err
		}
		data := append(buf, rest...)
		for {
			i := bytes.LastIndexByte(data, '\n')
			if i < 0 {
				break
			}
			if line := data[i+1:]; len(line) > 0 && visit(line) {
				return nil
			}
			data = data[:i]
		}
		rest = data
	}
	if pos == 0 && len(rest) > 0 {
		visit(rest)
	}
	return nil
}

// promptText is the text of a user entry the person typed: a string, or text blocks with no tool result among them.
func promptText(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return "", false
	}
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			return "", false
		case "text":
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n"), len(parts) > 0
}

// LastTurn reads the last turn of the main thread: everything after the last prompt a person typed. ok is false when
// the tail holds no such prompt.
func LastTurn(path string) (t Turn, ok bool, err error) {
	var uses []Use
	err = backwards(path, func(line []byte) bool {
		var e entry
		if json.Unmarshal(line, &e) != nil || e.Sidechain || e.Meta {
			return false
		}
		switch e.Type {
		case "user":
			if p, isPrompt := promptText(e.Message.Content); isPrompt {
				slices.Reverse(uses)
				t, ok = Turn{Prompt: p, Uses: uses}, true
				return true
			}
		case "assistant":
			var blocks []struct {
				Type  string          `json:"type"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			if json.Unmarshal(e.Message.Content, &blocks) != nil {
				return false
			}
			for j := len(blocks) - 1; j >= 0; j-- { // the walk is backwards: the whole list is reversed at the end
				if blocks[j].Type == "tool_use" {
					uses = append(uses, Use{blocks[j].Name, blocks[j].Input})
				}
			}
		}
		return false
	})
	return t, ok, err
}

// Used is the context a conversation holds now: the last main-thread assistant message's input, cache read and cache
// creation tokens. 0 when the tail has no usage.
func Used(path string) (n int, err error) {
	err = backwards(path, func(line []byte) bool {
		if !bytes.Contains(line, []byte(`"usage"`)) {
			return false
		}
		var e entry
		if json.Unmarshal(line, &e) != nil || e.Type != "assistant" || e.Sidechain || e.Message.Usage == nil {
			return false
		}
		u := e.Message.Usage
		n = u.In + u.CacheRead + u.CacheCreate
		return n > 0
	})
	return n, err
}
