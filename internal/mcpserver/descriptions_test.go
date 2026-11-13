package mcpserver

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
)

// M, 2026-11-12: "description mcp endpointu nevirsyk 1500 chars". What does not fit goes to `chatlog instructions`.
const maxChars = 1500

// describe is everything a client reads to use the tool: its description and the description of each parameter.
// The parameter names are not in it, so a rule has to be in the words.
func describe(t *testing.T, tool mcp.Tool) string {
	t.Helper()
	var parts []string
	for name, p := range tool.InputSchema.Properties {
		m, ok := p.(map[string]any)
		if !ok {
			t.Fatalf("%s.%s: a property of type %T: the size check would skip it", tool.Name, name, p)
		}
		parts = append(parts, fmt.Sprint(m["description"]))
	}
	sort.Strings(parts)
	return tool.Description + " " + strings.Join(parts, " ")
}

func TestEveryToolsTextAndTheInstructionsFitIn1500Characters(t *testing.T) {
	tools := newServer(nil, "test").ListTools()
	if len(tools) == 0 {
		t.Fatal("no tools")
	}
	for name, tool := range tools {
		if n := utf8.RuneCountInString(tool.Tool.Description); n > maxChars {
			t.Errorf("%s: description is %d characters, over %d", name, n, maxChars)
		}
		// the parameters are read with the description: the whole stays under the same bound
		n := utf8.RuneCountInString(describe(t, tool.Tool))
		if n > maxChars {
			t.Errorf("%s: description and parameter descriptions together are %d characters, over %d", name, n, maxChars)
		}
		t.Logf("%s: description %d, with parameters %d characters", name, utf8.RuneCountInString(tool.Tool.Description), n)
	}
	if n := utf8.RuneCountInString(Core); n > maxChars {
		t.Errorf("the server instructions are %d characters, over %d", n, maxChars)
	}
}

// Shortening must not lose a rule: each phrase is a rule a session needs and has to stay in the words of the tool it belongs to.
func TestTheToolTextsStillCarryEachRule(t *testing.T) {
	tools := newServer(nil, "test").ListTools()
	rules := map[string][]string{
		"kv_read": {"under a prefix", "as they stood then", "`age`", "stale: true", "12h", "expires_at", "deleted keys", "kv_link edges", "linked to it",
			"no value", "written or deleted since", "has_body", "its text comes with full 1"},
		"kv_write": {"one transaction", "200 bytes", "500", "5 segments", "expires", "soft", "if_absent", "if_value", "same transaction", "and project",
			"body {key: text}", "8 KiB", "head", "drops it"},
		"kv_search":   {"by meaning", "the head (the set value)", "has_body", "with_deleted 1", "30 s"},
		"kv_link":     {"one transaction", "append-only", "tombstone"},
		"chat_search": {"degraded", "the user's words", "start of that day", "exclusive", "subagent", "not by meaning alone"},
		"chat_get":    {"next_offset", "offset + length"},
		"chat_tail":   {"next_before", "oldest first"},
	}
	for name, phrases := range rules {
		text := describe(t, tools[name].Tool)
		for _, p := range phrases {
			if !strings.Contains(text, p) {
				t.Errorf("%s: the text lost %q", name, p)
			}
		}
	}
	// every field the notes protocol has is a parameter, so a client's schema offers it
	params := map[string][]string{
		"kv_read":     {"keys", "prefixes", "at", "linked_to", "follow", "with_deleted", "stale", "full", "keys_only", "changed_since"},
		"kv_write":    {"set", "del", "expires", "if_absent", "if_value", "body"},
		"kv_search":   {"query", "prefix", "limit", "with_deleted"},
		"chat_search": {"query", "limit", "since", "until", "role", "repo"},
	}
	for name, want := range params {
		for _, p := range want {
			if _, ok := tools[name].Tool.InputSchema.Properties[p]; !ok {
				t.Errorf("%s has no parameter %s", name, p)
			}
		}
	}
	got := make([]string, 0, len(tools))
	for name := range tools {
		got = append(got, name)
	}
	slices.Sort(got)
	t.Logf("tools: %v", got)
}
