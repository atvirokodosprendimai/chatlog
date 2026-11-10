// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package wake makes the recovery digest a session gets when it starts, after a compaction or after /clear: the
// first-class rule, the session's own notes, the latest exchanges with their tool calls as one-line heads, and the exact
// calls that fetch more. It reads the store only, is bounded by a byte cap, and is the same text `chatlog wake` prints.
package wake

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/playbook"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
	"github.com/atvirokodosprendimai/chatlog/internal/tei"
)

// The kinds of wake and their weights. A compaction lost the conversation: it gets the most. A /clear was asked for to
// start small: notes only. A new session or a resume: the state and the last few turns.
const (
	Compact = "compact"
	Clear   = "clear"
	Start   = "start"
)

type weight struct {
	prefixes  []string
	exchanges int
	opening   string
}

var weights = map[string]weight{
	Compact: {[]string{"now/", "waits/", "task/open/", "day/", "thread/", "rule/", "known/", "trigger/", "tool/", "fact/"}, 12,
		"THE CONVERSATION WAS COMPACTED: what the summary dropped is below and in the history. Read it before acting, follow the pointers the task needs, and say in one line what it changes against the summary."},
	Start: {[]string{"now/", "waits/", "task/open/", "rule/", "tool/"}, 5,
		"A NEW OR RESUMED SESSION: the latest state of this project is below."},
	Clear: {[]string{"now/", "waits/", "task/open/", "rule/", "tool/"}, 0,
		"THE CONTEXT WAS CLEARED on purpose, to start small: the notes are below; fetch the past only when the task needs it."},
}

// Kinds lists the valid kinds.
var Kinds = []string{Compact, Clear, Start}

// KindOf maps a SessionStart source (startup, resume, clear, compact) to a kind.
func KindOf(source string) string {
	switch source {
	case "compact":
		return Compact
	case "clear":
		return Clear
	}
	return Start
}

// Digest is the text for a project, at most max bytes. Sections go in order of worth (rule, notes, exchanges, the
// pointers to fetch more); a section that does not fit is cut, and the cut says so.
func Digest(ctx context.Context, st *store.Store, kind, project string, now time.Time, limit int) (string, error) {
	w, ok := weights[kind]
	if !ok {
		return "", fmt.Errorf("kind %q: one of %s", kind, strings.Join(Kinds, ", "))
	}
	var b builder
	// the rule always comes whole: a cap below it and the opening grows to fit them
	b.max = max(limit, len(playbook.Rule)+len(w.opening)+800)
	b.add(playbook.Rule)
	name := st.Label(ctx, project) // the project as people name it: its first alias
	b.add(fmt.Sprintf("\nCHATLOG WAKE (%s), project %q, %s. %s\n", kind, name, now.UTC().Format(time.RFC3339), w.opening))

	notes, err := st.KVRead(ctx, project, nil, w.prefixes, time.Time{})
	if err != nil {
		return "", err
	}
	b.add(fmt.Sprintf("\n## Notes: kv_read prefixes %q (%d)\n", w.prefixes, len(notes)))
	for _, n := range notes {
		stale := ""
		if n.Stale {
			stale = " [STALE " + n.Age + ": check before saying]"
		}
		if !b.add(fmt.Sprintf("%s = %s%s\n", n.Key, oneLine(n.Value, 240), stale)) {
			break
		}
	}

	oldest := int64(0)
	if w.exchanges > 0 {
		recent, err := st.Recent(ctx, project, store.OriginMain, w.exchanges, 64<<10)
		if err != nil {
			return "", err
		}
		b.add(fmt.Sprintf("\n## Latest %d exchanges of %q, oldest first: chat_get id=N fetches one whole, tool output included\n", len(recent), name))
		for _, e := range recent {
			if oldest == 0 {
				oldest = e.ID
			}
			if !b.add(head(e)) {
				break
			}
		}
	}

	more := fmt.Sprintf("\n## More\nchat_search query=\"<the user's words>\" finds what was said; kv_search query=\"<subject>\" finds notes; chat_tail project=%q", name)
	if oldest > 0 {
		more += fmt.Sprintf(" before=%d pages back from the oldest exchange above", oldest)
	} else {
		more += " reads the latest exchanges"
	}
	b.reserve(more + ".\n")
	return b.String(), nil
}

// head is one exchange as a few lines: who said what, each tool call with its time, duration and input head, the answer.
func head(e store.Tailed) string {
	var s strings.Builder
	who := "user"
	if e.Agent != "" {
		who = "user to agent " + e.Agent
	}
	fmt.Fprintf(&s, "#%d %s %s: %s\n", e.ID, e.Time.UTC().Format("2006-01-02 15:04Z"), who, oneLine(e.Words, 200))
	calls := toolCalls(e.Text)
	for i, c := range calls {
		if i == 8 {
			fmt.Fprintf(&s, "   ... %d more tool calls (chat_get id=%d)\n", len(calls)-8, e.ID)
			break
		}
		fmt.Fprintf(&s, "   tool %s\n", c)
	}
	if e.Answer != "" {
		fmt.Fprintf(&s, "   answer: %s\n", oneLine(e.Answer, 200))
	}
	return s.String()
}

// toolCalls reads the tool lines of a stored exchange text: "[tool Bash 09:00:01Z 2.5s error]" and its input's first line.
func toolCalls(text string) []string {
	var out []string
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, "[tool ") || !strings.HasSuffix(l, "]") {
			continue
		}
		c := strings.TrimSuffix(strings.TrimPrefix(l, "[tool "), "]")
		if i+1 < len(lines) && lines[i+1] != "" {
			c += ": " + oneLine(lines[i+1], 100)
		}
		out = append(out, c)
	}
	return out
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return tei.Clip(s, n) + "…"
}

// builder keeps the digest under its cap, with room held back for the closing pointers.
type builder struct {
	strings.Builder
	max int
	cut bool
}

const cutNote = "[cut at the digest's cap: fetch the rest with the calls below]\n"

// add appends s when it fits; once something did not fit, nothing more is added and the cut is said once.
func (b *builder) add(s string) bool {
	room := b.max - len(cutNote) - 400 // 400: the closing pointers
	if b.cut || b.Len()+len(s) > room {
		if !b.cut {
			b.WriteString(cutNote)
			b.cut = true
		}
		return false
	}
	b.WriteString(s)
	return true
}

// reserve appends the closing text, which always comes, within the cap.
func (b *builder) reserve(s string) {
	if b.Len()+len(s) > b.max {
		s = tei.Clip(s, max(b.max-b.Len(), 0))
	}
	b.WriteString(s)
}
