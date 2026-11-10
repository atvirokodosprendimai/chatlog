// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package know reads the user's own rules out of the notes (kv) and applies them: the matchers behind the hook pieces
// notes, prompt, karma, check and context. It knows key shapes, never content: a new rule is a kv_write, not a code
// change. Everything here is pure over notes, a tool call or a transcript file; the hook package does the I/O.
//
// A situation is a cause and its consequence, a program in the plain sense: sit/<g> holds "what | move"; sit/<g>/sig/<n>
// = "TOOLRX :: INRX" fires it when a tool call looks like that, sit/<g>/words/<n> = a regex fires it when a prompt does;
// the keys linked to sit/<g> (kv_link ... applies_to sit/<g>) are the rules that come with it.
package know

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/atvirokodosprendimai/chatlog/internal/search"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

// The key shapes.
const (
	PromptPrefix = "hook/prompt/" // every value printed with each prompt
	SitPrefix    = "sit/"         // sit/<g>, sit/<g>/sig/<n>, sit/<g>/words/<n>
	CheckPrefix  = "hook/check/"  // skip/<n>, change/<n>, bare, rule/<n>
	LibLegend    = "legend/lib/"  // legend/lib/<n> linked to a situation: every lib/<n>/* arrives with it, once a session
	LibPrefix    = "lib/"
)

// Recall: what a prompt's own words fetch from the notes. Measured on real notes: the right note for a five-word question
// scored 0.45 and noise for a two-word reply 0.48, so the cut ranks and short replies are skipped.
const (
	MinScore = 0.42
	Top      = 3
	MinWords = 3
)

var (
	sigKey   = regexp.MustCompile(`^sit/([^/]+)/sig/[^/]+$`)
	wordsKey = regexp.MustCompile(`^sit/([^/]+)/words/[^/]+$`)
)

// Sig is one signature of a situation: the tool (a full match on its name) and a pattern for its input.
type Sig struct {
	Group string
	Tool  *regexp.Regexp
	In    *regexp.Regexp
}

// Word is one prompt pattern of a situation.
type Word struct {
	Group string
	RX    *regexp.Regexp
}

// toolRX compiles a pattern that must match a whole tool name.
func toolRX(s string) (*regexp.Regexp, error) { return regexp.Compile(`^(?:` + s + `)$`) }

// inRX compiles an input pattern: case-insensitive, ^ and $ at each line.
func inRX(s string) (*regexp.Regexp, error) { return regexp.Compile(`(?im)` + s) }

// ParseSigs reads the signatures out of notes (any keys; others are ignored), in key order. bad names the keys whose
// value does not read as "TOOLRX :: INRX" with two patterns Go's RE2 accepts (no lookahead or backreference).
func ParseSigs(kvs []store.KV) (sigs []Sig, bad []string) {
	for _, e := range kvs {
		m := sigKey.FindStringSubmatch(e.Key)
		if m == nil {
			continue
		}
		parts := strings.SplitN(e.Value, " :: ", 2)
		if len(parts) != 2 {
			bad = append(bad, e.Key)
			continue
		}
		t, err1 := toolRX(strings.TrimSpace(parts[0]))
		in, err2 := inRX(strings.TrimSpace(parts[1]))
		if err1 != nil || err2 != nil {
			bad = append(bad, e.Key)
			continue
		}
		sigs = append(sigs, Sig{m[1], t, in})
	}
	return sigs, bad
}

// MatchSigs returns the groups with a signature that fits the call, each once, in key order.
func MatchSigs(sigs []Sig, tool, text string) []string {
	var hit []string
	for _, s := range sigs {
		if !slices.Contains(hit, s.Group) && s.Tool.MatchString(tool) && s.In.MatchString(text) {
			hit = append(hit, s.Group)
		}
	}
	return hit
}

// ParseWords reads the prompt patterns out of notes, case-insensitive; bad as in ParseSigs.
func ParseWords(kvs []store.KV) (words []Word, bad []string) {
	for _, e := range kvs {
		m := wordsKey.FindStringSubmatch(e.Key)
		if m == nil {
			continue
		}
		rx, err := regexp.Compile(`(?i)` + e.Value)
		if err != nil || strings.TrimSpace(e.Value) == "" {
			bad = append(bad, e.Key)
			continue
		}
		words = append(words, Word{m[1], rx})
	}
	return words, bad
}

// MatchWords returns the groups with a pattern that fits the prompt, each once, in key order.
func MatchWords(words []Word, prompt string) []string {
	var hit []string
	for _, w := range words {
		if !slices.Contains(hit, w.Group) && w.RX.MatchString(prompt) {
			hit = append(hit, w.Group)
		}
	}
	return hit
}

// ToolText is what a signature is matched against: the command itself for Bash (so a newline is a newline and ^ a line
// start), the input's JSON for any other tool.
func ToolText(tool string, input json.RawMessage) string {
	if tool == "Bash" {
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &in) == nil && in.Command != "" {
			return in.Command
		}
	}
	return compact(input)
}

func compact(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(b)
}

// Situation is the line of one recognised situation: its note (what | move) and the keys of the rules linked to it.
func Situation(group, what string, linked []string) string {
	s := SitPrefix + group
	if what != "" {
		s += " " + what
	}
	if len(linked) > 0 {
		s += " | rules: " + strings.Join(linked, " ")
	}
	return s
}

// Lib names the libraries among the linked keys (legend/lib/<n>).
func Lib(linked []string) []string {
	var out []string
	for _, k := range linked {
		if n, ok := strings.CutPrefix(k, LibLegend); ok && n != "" && !strings.Contains(n, "/") {
			out = append(out, n)
		}
	}
	return out
}

// LibText is a library printed whole: every lib/<n>/* note, one line.
func LibText(name string, kvs []store.KV) string {
	parts := make([]string, len(kvs))
	for i, e := range kvs {
		parts[i] = e.Key + ": " + e.Value
	}
	return LibPrefix + name + " (loaded once this session): " + strings.Join(parts, " ;; ")
}

// Notes is the always-on lines: every value under hook/prompt/, in key order.
func Notes(kvs []store.KV) string {
	var lines []string
	for _, e := range kvs {
		if strings.HasPrefix(e.Key, PromptPrefix) {
			lines = append(lines, e.Key+": "+e.Value)
		}
	}
	return strings.Join(lines, "\n")
}

// WantRecall says whether a prompt carries enough words to search the notes with.
func WantRecall(prompt string) bool { return len(strings.Fields(prompt)) >= MinWords }

// PickRecall keeps the hits worth printing: not the code map and not the always-on notes (already printed), at or over
// MinScore, the first Top.
func PickRecall(hits []search.KVHit) []search.KVHit {
	var out []search.KVHit
	for _, h := range hits {
		if strings.HasPrefix(h.Key, "map/") || strings.HasPrefix(h.Key, PromptPrefix) || h.Score < MinScore {
			continue
		}
		if out = append(out, h); len(out) == Top {
			break
		}
	}
	return out
}

// Recall prints the picked hits, and under them a line naming the stale ones: a note that went old must be checked
// against the source, then rewritten or deleted.
func Recall(hits []search.KVHit) string {
	if len(hits) == 0 {
		return ""
	}
	lines := []string{"kv for your words (a hit is a pointer, not the state: check it before saying it):"}
	var stale []string
	for _, h := range hits {
		mark := ""
		if h.Stale {
			mark = " (STALE " + h.Age + ")"
			stale = append(stale, h.Key)
		}
		lines = append(lines, fmt.Sprintf("%.2f %s%s: %s", h.Score, h.Key, mark, h.Value))
	}
	if len(stale) > 0 {
		lines = append(lines, "STALE NOTES TOUCH THIS PROMPT: check "+strings.Join(stale, ", ")+" against the source (git, host, code) before acting or saying it; then rewrite or delete the note in the same kv_write.")
	}
	return strings.Join(lines, "\n")
}
