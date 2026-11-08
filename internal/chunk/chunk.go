// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package chunk makes the two texts of an exchange: the full one kept in SQLite and the embedded one, which fits the model.
//
// f(exchange, limit) -> full, embed. The user's words and all assistant text are never cut. Tool output goes first, longest first, from
// the middle; a tool's input only if that was not enough; a run of steps collapses if even the markers do not fit.
package chunk

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atvirokodosprendimai/chatlog/internal/transcript"
)

// DefaultLimit is the token limit of an embedded text: 4000 fits an 8192-token model such as bge-m3 with room to spare.
const DefaultLimit = 4000

// Chunk is the result of Build.
type Chunk struct {
	Full      string
	Embed     string
	CutBytes  int  // bytes left out of Embed
	OverLimit bool // words and assistant text alone exceed the limit: nothing more may be cut, the model truncates
}

// Tokens is the count used here: a simple bound of bytes/3. Measured against TEI /tokenize, see the README.
func Tokens(s string) int { return len(s) / 3 }

func label(p transcript.Part) string {
	switch p.Kind {
	case transcript.ToolCall:
		return "[tool " + CallInfo(p) + "]\n"
	case transcript.ToolOutput:
		if p.Error {
			return "[output error]\n"
		}
		return "[output]\n"
	case transcript.Machine:
		return "[machine]\n"
	}
	return "[assistant]\n"
}

// CallInfo is a tool call as one line names it: "Bash 09:00:03Z 2.1s error", the parts it has.
func CallInfo(p transcript.Part) string {
	s := p.Name
	if !p.Time.IsZero() {
		s += " " + p.Time.UTC().Format("15:04:05Z")
	}
	if p.Took > 0 {
		s += " " + p.Took.Round(100*time.Millisecond).String()
	}
	if p.Error {
		s += " error"
	}
	return s
}

func head(e transcript.Exchange) string {
	s := "User: " + e.Words + "\n\n"
	if e.Agent != "" {
		s = "User (to agent " + e.Agent + "): " + e.Words + "\n\n"
	}
	return s
}

// full is chronological: what happened, in order.
func full(e transcript.Exchange) string {
	var b strings.Builder
	b.WriteString(head(e))
	for _, p := range e.Parts {
		b.WriteString(label(p))
		b.WriteString(p.Text)
		b.WriteString("\n\n")
	}
	if e.Answer != "" {
		b.WriteString("A: " + e.Answer + "\n")
	}
	return b.String()
}

// cutMiddle keeps head and tail of s within n bytes, marker included; below the marker's own size only the marker is left.
func cutMiddle(s string, n int) string {
	if len(s) <= n {
		return s
	}
	marker := fmt.Sprintf("[... cut %d bytes ...]", len(s))
	keep := n - len(marker)
	if keep <= 0 {
		return marker
	}
	h, t := keep/2, keep-keep/2
	for h > 0 && !utf8.RuneStart(s[h]) {
		h--
	}
	tail := len(s) - t
	for tail < len(s) && !utf8.RuneStart(s[tail]) {
		tail++
	}
	return s[:h] + marker + s[tail:]
}

// fitCap finds the largest cap at which the cut texts of idx fit budget bytes; -1 if not even markers do. Longest first follows
// from one cap for all: only what is longer than the cap is touched.
func fitCap(parts []transcript.Part, idx []int, budget int) int {
	size := func(c int) int {
		n := 0
		for _, i := range idx {
			n += len(cutMiddle(parts[i].Text, c))
		}
		return n
	}
	if size(0) > budget {
		return -1
	}
	lo, hi := 0, 0
	for _, i := range idx {
		hi = max(hi, len(parts[i].Text))
	}
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if size(mid) <= budget {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// Build makes both texts. The embedded text puts words and answer first: the model reads from the start, and if it truncates
// an over-limit text the steps are what it loses.
func Build(e transcript.Exchange, limit int) Chunk {
	c := Chunk{Full: full(e)}
	maxBytes := limit * 3
	top := head(e)
	if e.Answer != "" {
		top += "A: " + e.Answer + "\n\n"
	}
	parts := append([]transcript.Part(nil), e.Parts...)
	used := len(top)
	var outputs, calls []int
	for i, p := range parts {
		used += len(label(p)) + 2
		switch p.Kind {
		case transcript.ToolOutput, transcript.Machine:
			outputs = append(outputs, i)
		case transcript.ToolCall:
			calls = append(calls, i)
		default:
			used += len(p.Text)
		}
	}
	textLen := func(idx []int) int {
		n := 0
		for _, i := range idx {
			n += len(parts[i].Text)
		}
		return n
	}
	apply := func(idx []int, cp int) {
		for _, i := range idx {
			if cut := cutMiddle(parts[i].Text, cp); len(cut) != len(parts[i].Text) {
				c.CutBytes += len(parts[i].Text) - len(cut)
				parts[i].Text = cut
			}
		}
	}
	if used+textLen(calls)+textLen(outputs) > maxBytes {
		if cp := fitCap(parts, outputs, maxBytes-used-textLen(calls)); cp >= 0 {
			apply(outputs, cp)
		} else {
			apply(outputs, 0)
			if cp := fitCap(parts, calls, maxBytes-used-textLen(outputs)); cp >= 0 {
				apply(calls, cp)
			} else {
				apply(calls, 0)
			}
		}
	}
	var b strings.Builder
	b.WriteString(top)
	render := func(p transcript.Part) string { return label(p) + p.Text + "\n\n" }
	total := used + textLen(calls) + textLen(outputs)
	if total > maxBytes {
		parts, total = collapse(parts, total, maxBytes, &c)
	}
	for _, p := range parts {
		b.WriteString(render(p))
	}
	c.Embed = b.String()
	c.OverLimit = Tokens(c.Embed) > limit
	return c
}

// collapse drops tool steps from the middle out until the text fits, leaving one marker. Assistant text stays where it is.
func collapse(parts []transcript.Part, total, maxBytes int, c *Chunk) ([]transcript.Part, int) {
	mid := len(parts) / 2
	dropped, droppedBytes := 0, 0
	drop := make([]bool, len(parts))
	const markerRoom = 60
	for d := 0; d <= len(parts) && total+markerRoom > maxBytes; d++ {
		for _, i := range []int{mid - d, mid + d} {
			if i < 0 || i >= len(parts) || drop[i] || parts[i].Kind == transcript.Text || total+markerRoom <= maxBytes {
				continue
			}
			drop[i] = true
			n := len(label(parts[i])) + len(parts[i].Text) + 2
			total -= n
			droppedBytes += n
			dropped++
		}
	}
	if dropped == 0 {
		return parts, total
	}
	c.CutBytes += droppedBytes
	var out []transcript.Part
	marked := false
	for i, p := range parts {
		if !drop[i] {
			out = append(out, p)
			continue
		}
		if !marked {
			marked = true
			out = append(out, transcript.Part{Kind: transcript.Machine, Text: fmt.Sprintf("[... %d tool steps cut, %d bytes ...]", dropped, droppedBytes)})
		}
	}
	return out, total + markerRoom
}
