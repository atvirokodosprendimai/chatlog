// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package chunk

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/atvirokodosprendimai/chatlog/internal/transcript"
)

func ex(parts ...transcript.Part) transcript.Exchange {
	return transcript.Exchange{Words: "M words here", Answer: "the answer", Parts: parts}
}

func out(n int) transcript.Part {
	return transcript.Part{Kind: transcript.ToolOutput, Text: strings.Repeat("o", n)}
}

func TestUnderLimitIsWhole(t *testing.T) {
	c := Build(ex(transcript.Part{Kind: transcript.ToolCall, Name: "Bash", Text: "ls"}, out(50)), 4000)
	if c.CutBytes != 0 || c.OverLimit || strings.Contains(c.Embed, "cut") {
		t.Fatalf("%+v", c)
	}
	if !strings.Contains(c.Embed, "ls") || !strings.Contains(c.Full, "M words here") || !strings.Contains(c.Full, "the answer") {
		t.Fatalf("%+v", c)
	}
}

func TestOutputCutInTheMiddleLongestFirst(t *testing.T) {
	long := transcript.Part{Kind: transcript.ToolOutput, Text: "HEAD" + strings.Repeat("x", 40000) + "TAIL"}
	short := transcript.Part{Kind: transcript.ToolOutput, Text: "shortoutput"}
	c := Build(ex(long, short), 4000)
	if Tokens(c.Embed) > 4000 {
		t.Fatalf("over: %d", Tokens(c.Embed))
	}
	for _, want := range []string{"HEAD", "TAIL", "shortoutput", "cut "} {
		if !strings.Contains(c.Embed, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if c.CutBytes < 25000 {
		t.Fatalf("cut %d", c.CutBytes)
	}
	if !strings.Contains(c.Full, strings.Repeat("x", 40000)) {
		t.Fatal("full text lost bytes")
	}
}

func TestWordsTextAndAnswerNeverCut(t *testing.T) {
	words := strings.Repeat("w", 3000)
	answer := strings.Repeat("a", 3000)
	text := strings.Repeat("t", 3000)
	c := Build(transcript.Exchange{Words: words, Answer: answer, Parts: []transcript.Part{
		out(90000), {Kind: transcript.Text, Text: text}, {Kind: transcript.ToolCall, Name: "Write", Text: strings.Repeat("c", 90000)},
	}}, 4000)
	for _, want := range []string{words, answer, text} {
		if !strings.Contains(c.Embed, want) {
			t.Fatal("protected text was cut")
		}
	}
	if Tokens(c.Embed) > 4000 || c.OverLimit {
		t.Fatalf("tokens %d over %v", Tokens(c.Embed), c.OverLimit)
	}
}

// A thousand small steps cannot fit even as markers: they collapse, the words stay.
func TestManyStepsCollapse(t *testing.T) {
	var parts []transcript.Part
	for i := 0; i < 2000; i++ {
		parts = append(parts, transcript.Part{Kind: transcript.ToolCall, Name: "Bash", Text: "cmd"}, out(100))
	}
	c := Build(ex(parts...), 4000)
	if Tokens(c.Embed) > 4000 || c.OverLimit {
		t.Fatalf("tokens %d", Tokens(c.Embed))
	}
	if !strings.Contains(c.Embed, "tool steps cut") {
		t.Fatal("no collapse marker")
	}
}

func TestProtectedTextOverLimitIsFlagged(t *testing.T) {
	c := Build(transcript.Exchange{Words: strings.Repeat("w", 20000), Answer: "a"}, 4000)
	if !c.OverLimit || !strings.Contains(c.Embed, strings.Repeat("w", 20000)) {
		t.Fatalf("%v", c.OverLimit)
	}
}

func TestCutIsValidUTF8(t *testing.T) {
	c := Build(ex(transcript.Part{Kind: transcript.ToolOutput, Text: strings.Repeat("ąčę", 20000)}), 4000)
	if !utf8.ValidString(c.Embed) {
		t.Fatal("broken rune")
	}
}
