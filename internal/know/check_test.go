// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package know

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func use(name, input string) Use { return Use{name, json.RawMessage(input)} }

func bash(cmd string) Use {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return Use{"Bash", b}
}

func TestAudit(t *testing.T) {
	rules, bad := ParseRules(kvs(
		"hook/check/skip/1", "nightly tick",
		"hook/check/change/1", `git +push|deploy\.sh`,
		"hook/check/bare", "Status, na?",
		"hook/check/rule/1", `Bash :: (^|[|;&]\s*)(cat|head|sed -n)\s+\S+\.(go|md) :: file read through the shell`,
		"hook/check/rule/2", `Edit :: \.lock :: a lock file edited by hand`,
	))
	if len(bad) != 0 {
		t.Fatalf("bad %v", bad)
	}
	long := "please look at the failing build and tell me why it broke"
	for _, c := range []struct {
		name   string
		turn   Turn
		want   []string // substrings, one per miss, in order
		wantNo bool
	}{
		{"an edit with no kv_write", Turn{long, []Use{use("Edit", `{}`)}}, []string{"state changed (Edit x1) and no kv_write"}, false},
		{"the same with a kv_write", Turn{long, []Use{use("Edit", `{}`), use("mcp__chatlog__kv_write", `{}`)}}, nil, true},
		{"a cron tool is a change", Turn{long, []Use{use("CronCreate", `{}`), use("CronDelete", `{}`)}}, []string{"CronCreate x1, CronDelete x1"}, false},
		{"a shell command is a change only when a note says so", Turn{long, []Use{bash("ls"), bash("git push origin x")}}, []string{"Bash x1"}, false},
		{"a plain shell command is not", Turn{long, []Use{bash("ls")}}, nil, true},
		{"a short prompt with tools and no recall", Turn{"why?", []Use{bash("ls")}}, []string{"short prompt"}, false},
		{"a smile makes a long prompt short", Turn{long + " ;)", []Use{bash("ls")}}, []string{"short prompt"}, false},
		{"a short prompt with a recall", Turn{"why?", []Use{use("mcp__chatlog__chat_search", `{}`), bash("ls")}}, nil, true},
		{"kv_read counts as a recall", Turn{"why?", []Use{use("mcp__chatlog__kv_read", `{}`), bash("ls")}}, nil, true},
		{"a short prompt answered without tools", Turn{"why?", nil}, nil, true},
		{"a bare built-in answer", Turn{"OK.", []Use{bash("ls")}}, nil, true},
		{"a bare answer from the note", Turn{"Status!", []Use{bash("ls")}}, nil, true},
		{"six words is still short", Turn{"one two three four five six", []Use{bash("ls")}}, []string{"short prompt"}, false},
		{"a harness tag later in the prompt is not a skip", Turn{"see <system-reminder> then edit", []Use{bash("ls")}}, []string{"short prompt"}, false},
		{"seven words is not short", Turn{"one two three four five six seven", []Use{bash("ls")}}, nil, true},
		{"harness prompts are not audited", Turn{"<system-reminder> x", []Use{use("Edit", `{}`)}}, nil, true},
		{"task notifications neither", Turn{"<task-notification>x", []Use{use("Edit", `{}`)}}, nil, true},
		{"a prefix from the notes", Turn{"nightly tick 12", []Use{use("Edit", `{}`)}}, nil, true},
		{"a user rule", Turn{long, []Use{bash("cat main.go")}}, []string{"file read through the shell"}, false},
		{"a user rule after a pipe", Turn{long, []Use{bash("ls | head x.md")}}, []string{"file read through the shell"}, false},
		{"a user rule counted twice", Turn{long, []Use{bash("cat a.go"), bash("cat c.md")}}, []string{"file read through the shell (2 calls)"}, false},
		{"a user rule counted", Turn{long, []Use{bash("cat a.go"), bash("head b.md"), bash("cat c.md")}}, []string{"file read through the shell (3 calls)"}, false},
		{"a user rule needs its tool", Turn{long, []Use{use("Read", `{"command":"cat a.go"}`)}}, nil, true},
		{"a user rule on another tool", Turn{long, []Use{use("Edit", `{"file_path":"go.LOCK"}`), use("kv_write", `{}`)}}, []string{"a lock file edited by hand"}, false},
		{"all of them in order", Turn{"why?", []Use{use("Write", `{}`), bash("cat a.go")}}, []string{"state changed (Write x1)", "short prompt", "file read through the shell"}, false},
	} {
		got := Audit(c.turn, rules)
		if c.wantNo && len(got) != 0 {
			t.Errorf("%s: want clean, got %v", c.name, got)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: %v, want %d misses %v", c.name, got, len(c.want), c.want)
			continue
		}
		for i, w := range c.want {
			if !strings.Contains(got[i], w) {
				t.Errorf("%s: miss %d %q lacks %q", c.name, i, got[i], w)
			}
		}
	}
}

func TestBuiltInChecksNeedNoNotes(t *testing.T) {
	if got := Audit(Turn{"a long enough prompt to be about something", []Use{use("Edit", `{}`)}}, Rules{}); len(got) != 1 {
		t.Errorf("%v", got)
	}
}

func TestBadCheckNotesAreNamed(t *testing.T) {
	_, bad := ParseRules(kvs(
		"hook/check/rule/1", "Bash :: only two parts",
		"hook/check/rule/2", `Bash :: (?=x) :: lookahead`,
		"hook/check/rule/3", "Bash :: x :: ",
		"hook/check/change/1", `(`,
		"hook/check/rule/4", "Bash :: x :: fine :: with :: colons",
	))
	if !slices.Equal(bad, []string{"hook/check/rule/1", "hook/check/rule/2", "hook/check/rule/3", "hook/check/change/1"}) {
		t.Errorf("%v", bad)
	}
}

func TestMissed(t *testing.T) {
	if Missed(nil) != "" || Missed([]string{"a", "b"}) != "last turn missed: a; b" {
		t.Error(Missed([]string{"a", "b"}))
	}
}
