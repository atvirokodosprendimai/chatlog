# Hooks

`chatlog install` wires one command per event into Claude Code's `settings.json`:
`<path>/chatlog hook <Event>`, timeout 10 s, matcher `*` on the two tool events. Each run reads the event's JSON on
stdin, writes the SQLite file in the data dir itself (no server, no MCP needed), prints the context to inject, if any, as
`{"hookSpecificOutput":{"hookEventName":...,"additionalContext":...}}`, and exits 0 whatever happens: a failure goes to
`hooks.log` in the data dir. Its own budget is 3 s and it waits at most 1 s for the server's write lock; a row the
database still refuses goes to `spool.jsonl` in the data dir and the next hook writes it, so a lock costs a delay, not a
record.

Every stored text is redacted by shape first, then cut to `hooks.max_field_bytes` (default 2000) on a rune boundary with
a `…[+N bytes]` marker. `chatlog events` shows the records; `hooks.off` in `config.json` turns pieces off (named below).

| Event | Records (table `events`) | Injects | Pushes the transcript | Cost, measured |
|---|---|---|---|---|
| SessionStart | a row with `source` (startup, resume, clear, compact) | `recall`: the wake digest (`chatlog wake --kind compact` after a compaction, `clear` after /clear, `start` otherwise): the first-class rule, the notes, the latest exchanges with their tool calls, the fetch calls; capped at `hooks.wake_bytes` (24000) | yes, before the digest is made | ~20 ms + the digest's reads |
| UserPromptSubmit | a row with the prompt | `time`: `now <UTC> UTC \| <local> <zone> \| previous prompt 3m12s ago \| CHATLOG AND ITS MCP ARE A FIRST CLASS TOOL...` (the rule's first line) | yes: the previous turn closes | ~20 ms, plus the turn's read |
| PreToolUse | `tools`: opens the call's row: tool name, `tool_use_id`, start, input | nothing | no | ~20 ms |
| PostToolUse | `tools`: closes that row: `duration_ms`, output (a row of its own when PreToolUse left none) | `time`: `now <UTC> UTC \| call started HH:MM:SS <zone>, took N.NNN s` | no (see below) | ~20 ms |
| Stop | a row | nothing | yes: the turn's answer | ~20 ms + the turn's read |
| SubagentStop | a row | nothing | yes, and the subagent's own transcript | as Stop |
| PreCompact | a row with `trigger` (manual, auto) | nothing | yes: nothing is lost to the compaction | as Stop |
| SessionEnd | a row with `reason` | nothing | yes | as Stop |

**Pushing the transcript** (`ingest`) is the server's own idempotent step: read the file from its cursor, upsert each
exchange by the uuid of its opening message, move the cursor, all in one transaction, so two readers at once (a hook and
the server) double nothing. It runs once a turn, not at every tool call, because each read rebuilds the open exchange and
re-indexes its whole text: measured 150 ms for a turn of a few MB, against ~20 ms for any hook without it. The tool
rows already hold each call as it happens. A hook reads at most 8 MB of one transcript: a larger backlog (a transcript
from before the install, a huge turn) is left to the server's next scan or `chatlog ingest --all`, so no hook waits on it.
Vectors are made later by the server, only when an embedder is configured.

**A dotenv file** (`.env`, `.env.local`, `.envrc`, `prod.env`, ... named anywhere in the tool's input, also inside a
shell command after a space, a quote, `/`, `=`, `<`, `>`, `,` or `:`) keeps only its name: not the command, not what
was written, not what was read.

**Tool calls in the history**: besides the `events` rows, each exchange pushed from the transcript keeps every tool call
as `[tool Bash 09:00:01Z 2.5s error]` with its input and output, the time and duration read from the transcript's own
timestamps; `chat_get`, `chat_tail` and the wake digest show them.

**What is not recorded by the hooks**: the model's thinking (not in the transcript either). A hook that misses its 3 s
budget records nothing for that event (logged); the transcript ingest at the turn's end still holds the turn's text.

Costs were measured on Linux with a 27 MB transcript already ingested (wall clock around the whole process, 3 runs each, warm cache); a cold start of the
binary adds a few ms.
