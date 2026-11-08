---
name: chatlog
description: Recall from and write to the chatlog history (the chatlog MCP server). Use it on "recall", "what did we decide", "where did we stop", "why is this so", "did we try", after a compaction or /clear, whenever context feels lost or a summary is all you have, and whenever there is state, a decision or a lesson to note.
---
<!-- written by chatlog install; chatlog install --uninstall removes it -->

# chatlog

{{RULE}}
## Which tool for which question

| Question | Call |
|---|---|
| What did the user say about X, when, in which words | `chat_search` query = the user's own words (add `since`, `repo` to narrow) |
| The whole of one exchange, tool calls and output included | `chat_get` id = the hit's id; page with `next_offset` |
| What happened just before (a compaction, yesterday) | `chat_tail` project = this project; page back with `before` = `next_before` |
| My own notes: state, open tasks, rules, how a tool is used | `kv_read` prefixes `["now/","waits/","task/open/","rule/","tool/"]` |
| A note whose key I do not know | `kv_search` query = the subject |
| How the record stands (counts, last ingest, pending vectors) | `chat_status` |

## Writing as you go

- After every result, decision or finished step: one `kv_write` with `set: {"<key>": "<one line>"}` (and `del: [keys]`
  for what it replaces). No `key=`/`value=` arguments exist.
- Keys by kind: `now/` state (stale after 12 h), `waits/` what waits on the user, `task/open/<name>` "k/N done: next
  step", `task/done/<name>`, `rule/` the user's rules with their date, `known/` bugs and gaps, `fact/` checked facts,
  `tool/` how a tool is used.
- A value is one line under 200 bytes: the gist, where the payload lies (`chat:<id>`, `file:line`, `repo@sha`, a URL),
  and the date. The user's words go quoted and dated; your own conclusions are marked as claims.
- Relate notes with `kv_link` (`affects`, `blocked_by`, `fixed_by`, `decided_by`, `applies_to`, `supersedes`,
  `see_also`), not with "-> X" inside a value.

## After a compaction, a /clear or a new session

The session-start hook already injected a digest: the rule above, your notes and the latest turns with their tool
calls, each with the exact call that fetches it whole. Read it, follow the pointers the task needs, say in one line what
it changes against the summary, then go on. `chatlog wake --kind compact` prints the same digest in a shell.

The long form: docs/playbook.md in the chatlog repository (https://github.com/atvirokodosprendimai/chatlog).
