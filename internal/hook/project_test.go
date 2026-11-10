// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package hook

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/atvirokodosprendimai/chatlog/internal/search"
)

// kvIn writes notes the way the MCP server of a session in cwd would: for that directory's project.
func (r *rig) kvIn(project string, set map[string]string) {
	r.t.Helper()
	if _, err := r.d.Store.KVWrite(context.Background(), project, set, nil); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) preIn(cwd, tool, input string) string {
	r.t.Helper()
	out, err := r.run("PreToolUse", fmt.Sprintf(`{"session_id":"s1","cwd":%q,"tool_name":%q,"tool_use_id":"u","tool_input":%s}`, cwd, tool, input))
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

func (r *rig) promptIn(cwd, prompt string) string {
	r.t.Helper()
	out, err := r.run("UserPromptSubmit", fmt.Sprintf(`{"session_id":"s1","cwd":%q,"prompt":%q}`, cwd, prompt))
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

// One project's karma, its always-on lines and its recall are not another project's.
func TestOneProjectsRulesDoNotFireInAnother(t *testing.T) {
	r := newRig(t)
	r.kvIn("path:/u/alpha", map[string]string{"sit/rel": "release | alpha gate first", "sit/rel/sig/1": "Bash :: git push", "hook/prompt/1": "alpha always-on line"})
	r.kvIn("path:/u/beta", map[string]string{"sit/rel": "release | beta gate first", "sit/rel/sig/1": "Bash :: git push"})
	r.kvIn("global", map[string]string{"hook/prompt/9": "line for every project"})
	if got := r.preIn("/u/alpha", "Bash", `{"command":"git push"}`); !strings.Contains(got, "alpha gate first") || strings.Contains(got, "beta") {
		t.Errorf("alpha's own karma: %q", got)
	}
	if got := r.preIn("/u/beta", "Bash", `{"command":"git push"}`); !strings.Contains(got, "beta gate first") || strings.Contains(got, "alpha") {
		t.Errorf("beta's own karma: %q", got)
	}
	if got := r.preIn("/u/gamma", "Bash", `{"command":"git push"}`); got != "" {
		t.Errorf("a project that has no such situation: %q", got)
	}
	if got := r.promptIn("/u/beta", "anything"); strings.Contains(got, "alpha always-on") || !strings.Contains(got, "line for every project") {
		t.Errorf("beta's prompt: %q", got)
	}
	if got := r.promptIn("/u/alpha", "anything"); !strings.Contains(got, "alpha always-on") || !strings.Contains(got, "line for every project") {
		t.Errorf("alpha's prompt: %q", got)
	}
}

func TestRecallAsksForTheProjectOfTheSession(t *testing.T) {
	r := newRig(t)
	var asked []string
	r.d.Recall = func(_ context.Context, project, _ string, _ int) ([]search.KVHit, error) {
		asked = append(asked, project)
		return nil, nil
	}
	r.promptIn("/u/alpha", "what is the release gate here")
	if len(asked) != 1 || asked[0] != "path:/u/alpha" {
		t.Fatalf("recall asked %v", asked)
	}
}

// An agent working in one directory for another project says so with CHATLOG_PROJECT; hooks and tail agree on it.
func TestTheProjectVariableOverridesTheDirectory(t *testing.T) {
	r := newRig(t)
	r.kvIn("alpha-fleet", map[string]string{"sit/rel": "release | fleet gate", "sit/rel/sig/1": "Bash :: git push"})
	if got := r.preIn("/u/beta", "Bash", `{"command":"git push"}`); got != "" {
		t.Fatalf("without the variable the directory decides: %q", got)
	}
	t.Setenv("CHATLOG_PROJECT", "alpha-fleet")
	if got := r.preIn("/u/beta", "Bash", `{"command":"git push"}`); !strings.Contains(got, "fleet gate") {
		t.Fatalf("with it the named project does: %q", got)
	}
}
