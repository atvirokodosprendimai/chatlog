// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package playbook is the one source of how chatlog is meant to be used: the first-class rule every session sees (MCP
// initialize, instructions --core, each injected digest, the skill), the skill file the installer places, and the
// starter notes `chatlog seed` writes. docs/playbook.md is the long form of the same.
package playbook

import (
	_ "embed"
	"strings"
)

// Rule is the statement every session gets first, wherever chatlog speaks to it.
const Rule = `CHATLOG AND ITS MCP ARE A FIRST CLASS TOOL. LOSING CONTEXT IS EXPENSIVE. A TOOL CALL IS CHEAPER THAN LOST CONTEXT.
SEARCH AND WRITE THE HISTORY BEFORE YOU GUESS, AND KEEP STATE IN IT AS YOU GO.
  - Recall before answering: chat_search with the user's own words (kv_search for your notes), then say the hit's id or "nothing".
  - Write state as it happens: kv_write what you are doing, what is done and what you learned, in the same turn.
  - A hit is what was said then, not the state now: check it against the code before relying on it.
`

// FirstLine is the rule's first line, which every injection and doc must carry: tests and doctor look for it.
var FirstLine = strings.SplitN(Rule, "\n", 2)[0]

// SkillMarker is the line that says a SKILL.md is chatlog's own, so uninstall removes only that.
const SkillMarker = "<!-- written by chatlog install; chatlog install --uninstall removes it -->"

//go:embed skill.md
var skillBody string

// Skill is the SKILL.md the installer writes to <claude dir>/skills/chatlog/.
func Skill() string { return strings.Replace(skillBody, "{{RULE}}", Rule, 1) }

// Note is one starter note of `chatlog seed`.
type Note struct{ Key, Value string }

// SeedDate stamps the starter notes, so a reader sees they came with the install, not from the user.
const SeedDate = "2026-11-08"

// Seed is the starter set: the playbook's rules as notes, each a gist and where the rest lies, under 200 bytes.
var Seed = []Note{
	{"rule/chatlog-first-class", "RULE chatlog + its MCP = first class tool; a tool call is cheaper than lost context; state goes in as it happens (seed " + SeedDate + ") -> docs/playbook.md"},
	{"rule/recall-first", "RULE before answering a short, unclear or corrective message: chat_search the user's own words, kv_search the subject; say the hit id or 'nothing' (seed " + SeedDate + ")"},
	{"rule/hit-is-past", "RULE a hit is what was said then, not the state now: check it against the code or the system before saying it (seed " + SeedDate + ")"},
	{"rule/kv-every-step", "RULE after every result, decision or finished step: kv_write doing (now/), done (task/done/), learned (known/ tool/) in the same turn; never hold it only in context (seed " + SeedDate + ")"},
	{"rule/task-steps", "RULE each task = task/open/<name> 'k/N done: next step', updated after each step; done: set task/done/<name> + del the open key in one kv_write (seed " + SeedDate + ")"},
	{"rule/stale", "RULE now/ and waits/ older than 12 h are marked stale: read the source again before saying them; rewrite now/ at each task end (seed " + SeedDate + ")"},
	{"rule/value-format", "RULE a value = one line <=200 B: gist | pointer (chat:ID, file:line, repo@sha, URL) | date; the user's words quoted and dated, own claims marked claim (seed " + SeedDate + ")"},
	{"tool/kv-write", "kv_write takes set: {\"key\": \"one line\"} and del: [keys]; there are no key=/value= arguments; one bad entry writes nothing (seed " + SeedDate + ")"},
	{"tool/kv-link", "kv_link add [{s, p, o}] relates notes (p: affects blocked_by fixed_by decided_by applies_to supersedes see_also) instead of '-> X' inside a value (seed " + SeedDate + ")"},
	{"tool/recall", "recall: words said -> chat_search; own notes -> kv_read prefixes / kv_search; whole exchange -> chat_get id; the day before -> chat_tail back with next_before (seed " + SeedDate + ")"},
	{"tool/wake", "after compaction/clear/start the hook injects a digest (chatlog wake --kind compact|clear|start): notes + recent turns with tool calls; fetch more with the calls it names (seed " + SeedDate + ")"},
	{"legend/shelves", "kv prefixes: now/ state | waits/ on the user | task/open|done/ | rule/ the user's rules | known/ bugs, gaps | fact/ checked | tool/ how a tool is used | day/ thread/ (seed " + SeedDate + ")"},
	{"hook/prompt/01", "EXAMPLE of an always-on line: every key under hook/prompt/ is printed with each prompt; replace this value with your own rule, or del the key (seed " + SeedDate + ")"},
	{"sit/ask-first", "EXAMPLE situation: a force push, a drop or an rm -rf | stop, say what it will touch, and wait for the user's word (seed " + SeedDate + ")"},
	{"sit/ask-first/sig/1", `Bash :: git +push .*(--force|-f\b)|\brm +-rf\b|\bdrop +(table|database)\b`},
	{"sit/ask-first/words/1", `force.?push|\bdrop +(the +)?(table|database)\b|\brm +-rf\b`},
	{"hook/check/rule/01", `Bash :: (^|[|;&]\s*)(cat|head|tail)\s+[^|;&]*\.(go|py|js|ts|md|ya?ml|json)\b :: a file was read through the shell: use the file-reading tool (an example rule; edit or del it)`},
}
