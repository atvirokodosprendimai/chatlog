// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package know

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/atvirokodosprendimai/chatlog/internal/playbook"
	"github.com/atvirokodosprendimai/chatlog/internal/search"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

func kvs(pairs ...string) []store.KV {
	var out []store.KV
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, store.KV{Key: pairs[i], Value: pairs[i+1]})
	}
	return out
}

func TestSigsMatchTheToolWholeAndTheInputAsTheNoteSays(t *testing.T) {
	sigs, bad := ParseSigs(kvs(
		"sit/push/sig/1", `Bash :: git +push`,
		"sit/push/sig/2", `Bash :: push.*origin`,
		"sit/tests/sig/1", `Bash :: ^go test`, // a line start, in a command of several lines
		"sit/edit/sig/1", `Edit|Write :: \.env`,
		"sit/mcp/sig/1", `mcp__.*__kv_write :: "waits/`,
		"sit/other/words/1", `unrelated`, // not a signature
	))
	if len(bad) != 0 || len(sigs) != 5 {
		t.Fatalf("sigs %d bad %v", len(sigs), bad)
	}
	for _, c := range []struct {
		name, tool, text string
		want             []string
	}{
		{"input is case-insensitive", "Bash", "GIT PUSH origin main", []string{"push"}},
		{"two signatures of one group count once", "Bash", "git push origin main", []string{"push"}},
		{"the tool is a full match, not a prefix", "BashOutput", "git push", nil},
		{"a line start inside a command", "Bash", "cd x\ngo test ./...", []string{"tests"}},
		{"not at a line start", "Bash", "echo go test", nil},
		{"alternation of tools", "Write", `{"file_path":"/a/.env"}`, []string{"edit"}},
		{"another tool of the alternation", "Read", `{"file_path":"/a/.env"}`, nil},
		{"a tool by pattern", "mcp__chatlog__kv_write", `{"set":{"waits/x":"y"}}`, []string{"mcp"}},
		{"no match", "Bash", "ls", nil},
	} {
		if got := MatchSigs(sigs, c.tool, c.text); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBadSigsAreNamedNotDropped(t *testing.T) {
	sigs, bad := ParseSigs(kvs(
		"sit/a/sig/1", "no separator",
		"sit/b/sig/1", `Bash :: (?!x)lookahead`, // RE2 has none
		"sit/c/sig/1", `Bash :: ok`,
	))
	if len(sigs) != 1 || sigs[0].Group != "c" {
		t.Errorf("sigs %+v", sigs)
	}
	if !slices.Equal(bad, []string{"sit/a/sig/1", "sit/b/sig/1"}) {
		t.Errorf("bad %v", bad)
	}
}

func TestToolText(t *testing.T) {
	for _, c := range []struct{ name, tool, in, want string }{
		{"bash is the command", "Bash", `{"command":"a\nb"}`, "a\nb"},
		{"bash without a command is the json", "Bash", `{"x":1}`, `{"x":1}`},
		{"another tool is the compact json", "Edit", "{ \"file_path\" : \"a\" }", `{"file_path":"a"}`},
		{"not json stays as it is", "Edit", `nope`, `nope`},
	} {
		if got := ToolText(c.tool, json.RawMessage(c.in)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestWordsMatchThePromptCaseInsensitively(t *testing.T) {
	words, bad := ParseWords(kvs("sit/rel/words/1", `release|ship it`, "sit/rel/words/2", `deploy`, "sit/x/words/1", `(`, "sit/x/sig/1", "Bash :: a"))
	if !slices.Equal(bad, []string{"sit/x/words/1"}) || len(words) != 2 {
		t.Fatalf("words %d bad %v", len(words), bad)
	}
	if got := MatchWords(words, "Please SHIP IT and deploy"); !slices.Equal(got, []string{"rel"}) {
		t.Errorf("one group once: %v", got)
	}
	if got := MatchWords(words, "nothing"); got != nil {
		t.Errorf("%v", got)
	}
}

func TestSituationAndLibLines(t *testing.T) {
	if got := Situation("g", "what | move", []string{"rule/a", "legend/lib/x"}); got != "sit/g what | move | rules: rule/a legend/lib/x" {
		t.Errorf("%q", got)
	}
	if got := Situation("g", "", nil); got != "sit/g" {
		t.Errorf("%q", got)
	}
	if got := Lib([]string{"rule/a", "legend/lib/x", "legend/lib/y/z", "legend/lib/"}); !slices.Equal(got, []string{"x"}) {
		t.Errorf("%v", got)
	}
	got := LibText("x", kvs("lib/x/1", "cmd one", "lib/x/2", "cmd two"))
	if got != "lib/x (loaded once this session): lib/x/1: cmd one ;; lib/x/2: cmd two" {
		t.Errorf("%q", got)
	}
}

func TestNotesAreTheValuesUnderTheirPrefixInKeyOrder(t *testing.T) {
	got := Notes(kvs("hook/check/bare", "ok", "hook/prompt/01", "first", "hook/prompt/02", "second", "rule/x", "no"))
	if got != "hook/prompt/01: first\nhook/prompt/02: second" {
		t.Errorf("%q", got)
	}
	if Notes(nil) != "" {
		t.Error("no notes, no text")
	}
}

func hit(key string, score float32, stale bool) search.KVHit {
	return search.KVHit{KV: store.KV{Key: key, Value: "v", Stale: stale, Age: "13h"}, Score: score}
}

func TestPickRecallRanksCutsAndSkips(t *testing.T) {
	got := PickRecall([]search.KVHit{
		hit("map/a", 0.9, false), hit("hook/prompt/01", 0.9, false), hit("a", 0.8, false), hit("low", 0.41, false),
		hit("b", 0.42, false), hit("c", 0.5, false), hit("d", 0.6, false),
	})
	var keys []string
	for _, h := range got {
		keys = append(keys, h.Key)
	}
	if !slices.Equal(keys, []string{"a", "b", "c"}) {
		t.Errorf("%v: the first three at or over 0.42, no map/, no always-on note", keys)
	}
}

func TestWantRecallNeedsThreeWords(t *testing.T) {
	for p, want := range map[string]bool{"": false, "ok": false, "op taip": false, "what is t119": true, "  a\tb\nc ": true} {
		if WantRecall(p) != want {
			t.Errorf("%q", p)
		}
	}
}

func TestRecallNamesTheStaleOnes(t *testing.T) {
	got := Recall([]search.KVHit{hit("now/x", 0.5, true), hit("rule/y", 0.45, false)})
	for _, want := range []string{"0.50 now/x (STALE 13h): v", "0.45 rule/y: v", "check now/x against the source", "rewrite or delete"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from %q", want, got)
		}
	}
	if strings.Contains(Recall([]search.KVHit{hit("rule/y", 0.45, false)}), "STALE") || Recall(nil) != "" {
		t.Error("no stale note, no stale line; no hit, no text")
	}
}

// The starter notes are the documented examples: they must read, and do what their text says.
func TestStarterNotesWork(t *testing.T) {
	var all []store.KV
	for _, n := range playbook.Seed {
		all = append(all, store.KV{Key: n.Key, Value: n.Value})
	}
	sigs, bad := ParseSigs(all)
	words, badW := ParseWords(all)
	rules, badR := ParseRules(all)
	if len(bad)+len(badW)+len(badR) != 0 || len(sigs) != 1 || len(words) != 1 || len(rules.User) != 1 {
		t.Fatalf("bad %v %v %v; %d %d %d", bad, badW, badR, len(sigs), len(words), len(rules.User))
	}
	if !strings.HasPrefix(Notes(all), "hook/prompt/01: EXAMPLE") {
		t.Errorf("the always-on example: %q", Notes(all))
	}
	for _, cmd := range []string{"git push --force origin main", "git push -f", "rm -rf /tmp/x", "psql -c 'DROP TABLE users'"} {
		if got := MatchSigs(sigs, "Bash", cmd); !slices.Equal(got, []string{"ask-first"}) {
			t.Errorf("%q: %v", cmd, got)
		}
	}
	for _, cmd := range []string{"git push origin main", "ls -rf", "git status"} {
		if got := MatchSigs(sigs, "Bash", cmd); got != nil {
			t.Errorf("%q: %v", cmd, got)
		}
	}
	if got := MatchWords(words, "can I force-push this branch?"); !slices.Equal(got, []string{"ask-first"}) {
		t.Errorf("%v", got)
	}
	turn := Turn{Prompt: "please read the whole file and tell me what it does", Uses: []Use{bash("cat internal/a.go | head")}}
	if got := Audit(turn, rules); len(got) != 1 || !strings.Contains(got[0], "read through the shell") {
		t.Errorf("%v", got)
	}
	if got := Audit(Turn{Prompt: turn.Prompt, Uses: []Use{bash("mrw read internal/a.go")}}, rules); len(got) != 0 {
		t.Errorf("%v", got)
	}
}
