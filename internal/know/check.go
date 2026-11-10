// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package know

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

var ruleKey = regexp.MustCompile(`^hook/check/rule/[^/]+$`)

// Checks audit the last turn when it ends. They never block: what a turn missed is printed at the next prompt.
//
// Built in, because chatlog owns these tools: a state change with no kv_write, a short prompt answered without a recall.
// The user's own checks are notes: hook/check/rule/<n> = "TOOLRX :: INRX :: message" says "a call like this is a miss".
var (
	// builtinSkip are prompts that are not the person's: the harness speaking.
	builtinSkip = []string{"<system-reminder>", "<task-notification>", "<command-message>", "<command-name>"}
	// builtinBare are answers that carry no subject to recall; hook/check/bare adds to them.
	builtinBare = []string{"ok", "okay", "yes", "no", "y", "n", "thanks", "continue"}
	// changeTools are the tools that change state by themselves; Cron* too. A shell command is a change only when a
	// hook/check/change/<n> pattern says so.
	changeTools = []string{"Edit", "Write", "MultiEdit", "NotebookEdit"}
	recallTools = []string{"chat_search", "kv_search", "kv_read"}
)

// UserRule is one hook/check/rule/<n>.
type UserRule struct {
	Tool, In *regexp.Regexp
	Msg      string
}

// Rules are the checks the notes add to the built-in ones.
type Rules struct {
	Skip   []string         // hook/check/skip/<n>: a prompt starting with it is not audited
	Change []*regexp.Regexp // hook/check/change/<n>: a Bash command like this changed state
	Bare   []string         // hook/check/bare: a comma list of answers that need no recall
	User   []UserRule
}

// ParseRules reads the check notes (other keys are ignored); bad names those that do not read.
func ParseRules(kvs []store.KV) (r Rules, bad []string) {
	for _, e := range kvs {
		switch {
		case strings.HasPrefix(e.Key, CheckPrefix+"skip/"):
			r.Skip = append(r.Skip, e.Value)
		case strings.HasPrefix(e.Key, CheckPrefix+"change/"):
			if rx, err := inRX(e.Value); err == nil {
				r.Change = append(r.Change, rx)
			} else {
				bad = append(bad, e.Key)
			}
		case e.Key == CheckPrefix+"bare":
			for _, w := range strings.Split(e.Value, ",") {
				if w = normalize(w); w != "" {
					r.Bare = append(r.Bare, w)
				}
			}
		case ruleKey.MatchString(e.Key):
			parts := strings.SplitN(e.Value, " :: ", 3)
			if len(parts) != 3 {
				bad = append(bad, e.Key)
				continue
			}
			t, err1 := toolRX(strings.TrimSpace(parts[0]))
			in, err2 := inRX(strings.TrimSpace(parts[1]))
			if err1 != nil || err2 != nil || strings.TrimSpace(parts[2]) == "" {
				bad = append(bad, e.Key)
				continue
			}
			r.User = append(r.User, UserRule{t, in, strings.TrimSpace(parts[2])})
		}
	}
	return r, bad
}

func normalize(s string) string { return strings.ToLower(strings.Trim(strings.TrimSpace(s), ".!?")) }

// tool is a tool's name without its MCP server prefix: mcp__chatlog__kv_write is kv_write.
func tool(name string) string {
	if i := strings.LastIndex(name, "__"); strings.HasPrefix(name, "mcp__") && i >= 0 {
		return name[i+2:]
	}
	return name
}

// Short says whether a prompt is the kind that needs a recall before an answer: six words or fewer, or a smile.
func Short(prompt string) bool {
	return len(strings.Fields(prompt)) <= 6 || strings.Contains(prompt, ";)") || strings.Contains(prompt, ":D")
}

// Audit returns what the turn missed, one line each; none when it is clean or when the prompt is not audited.
func Audit(t Turn, r Rules) []string {
	p := strings.TrimSpace(t.Prompt)
	for _, pre := range slices.Concat(builtinSkip, r.Skip) {
		if pre != "" && strings.HasPrefix(p, pre) {
			return nil
		}
	}
	var miss []string
	changed, wrote, recalled := map[string]int{}, false, false
	for _, u := range t.Uses {
		n := tool(u.Name)
		switch {
		case n == "kv_write":
			wrote = true
		case slices.Contains(recallTools, n):
			recalled = true
		case slices.Contains(changeTools, n) || strings.HasPrefix(n, "Cron"):
			changed[n]++
		case n == "Bash":
			text := ToolText(u.Name, u.Input)
			if slices.ContainsFunc(r.Change, func(rx *regexp.Regexp) bool { return rx.MatchString(text) }) {
				changed["Bash"]++
			}
		}
	}
	if len(changed) > 0 && !wrote {
		names := make([]string, 0, len(changed))
		for n, c := range changed {
			names = append(names, fmt.Sprintf("%s x%d", n, c))
		}
		slices.Sort(names)
		miss = append(miss, "state changed ("+strings.Join(names, ", ")+") and no kv_write in the turn")
	}
	bare := slices.Contains(slices.Concat(builtinBare, r.Bare), normalize(p))
	if Short(p) && !bare && len(t.Uses) > 0 && !recalled {
		miss = append(miss, "a short prompt was answered with tool calls but no chat_search, kv_search or kv_read")
	}
	for _, ur := range r.User {
		n := 0
		for _, u := range t.Uses {
			if ur.Tool.MatchString(u.Name) && ur.In.MatchString(ToolText(u.Name, u.Input)) {
				n++
			}
		}
		if n > 1 {
			miss = append(miss, fmt.Sprintf("%s (%d calls)", ur.Msg, n))
		} else if n == 1 {
			miss = append(miss, ur.Msg)
		}
	}
	return miss
}

// Missed is what the next prompt prints.
func Missed(miss []string) string {
	if len(miss) == 0 {
		return ""
	}
	return "last turn missed: " + strings.Join(miss, "; ")
}
