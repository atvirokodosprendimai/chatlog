# Design: conditional kv writes, long notes, change feeds, search filters (2026-11-12)

M, 2026-11-12: "ko truksta chatlog memory tau?", "kokiu endpoint? filtru or so?", "tai pasitobulink", "description mcp endpointu nevirsyk 1500 chars".
Written before the code; the tests in `internal/*/` check each invariant below.

## 1. kv_write conditions: `if_absent`, `if_value`

Why: parallel agents claim a shared number (an ADR task id) by read-then-write, and two of them win. The check has to sit in the write's own transaction.

- `if_absent: [keys]`: refused when the key's last row is a live value. A key never written, or whose last row is a delete, is absent.
- `if_value: {key: expected}`: refused unless the key's last row is live and its head value equals `expected` exactly. An absent key never equals.
- The keys need not be in `set`/`del` (a guard on one key can protect a write of another). Each is a valid key; `expected` is not empty.
- Invariants: the check and the insert run in ONE write transaction (`BEGIN IMMEDIATE`: the write lock is taken first, so it holds against a second process on the file too),
  so of N writers claiming one key exactly one succeeds and each loser gets a refusal, not a database error.
  A refusal writes nothing (not even the keys that passed) and names EVERY failing key with its current value, its time and the project that wrote it:
  `refused: condition not met, nothing written: if_absent "k": exists, value "v", written <time> in project [abc1234] shop`. `if_value` on a key never written says so;
  on a deleted one it says `deleted <time> in project ...`. A global note says `in project global`.
- A wrong type for an argument (`if_absent` a string, `if_value` not an object), or a condition the server does not know (a misspelt `if_*` field), is a refusal and writes nothing: a guard is never dropped.
  Any other unknown field in `/kv/write` was ignored before; by the two-release rule it is still accepted, with a warning in the answer that names it, and a later release refuses it.
- Limits, written down: (1) a claim is not idempotent: a claimer whose answer was lost and who retries is refused by its own row; it sees its own value in the refusal, so
  put a token in the value (`claimed by <session>`) and treat "the holder is me" as success. (2) The guarantee holds among writers that send the conditions: a plain write from an
  older binary, or without `if_absent`, overwrites the key. (3) A new `chatlog mcp` talking to an OLDER server is refused for the conditions only if that server is this version
  or newer; an older server ignores unknown fields, so restart the server with the binary.
- Project: there is no separate "writer" column. A note belongs to a project in main's model (the `kv.project` column: the repository's root commit, `''` for global; the MCP server sends its own, resolved from its cwd, and a request with none resolves like the CLI). A condition is checked in the scope the key would be written to: the same key in another project is another note, and a `global/` key is one note for every project. The refusal names the project as `chatlog project` shows it: the short key and its first alias, or `global`. (An earlier draft of this branch added its own `kv.project` with the folder's base name; merged onto main it is main's column, so the two cannot disagree.)
- Compat: a request without the fields is checked and written as today. HTTP: a refusal is 400, like every refusal.

## 2. kv_read `keys_only`, `changed_since`

- `keys_only`: MCP `1`, HTTP `true` (like `stale` and `with_deleted`): each key has key, time, age, stale, `bytes` (the head's size; with a body, section 3, head plus body: what a full read costs) and, with a body, `has_body`; no `value`, no `body`. Combines with keys, prefixes, stale, at, with_deleted, follow.
- `changed_since: <RFC3339 | span 1h, 7d>`: a span counts back from now, or from `at` when the notes are read as they stood then (as `age` does); it is positive and at most 36500 d. Only keys whose last row (a write or a delete) is at or after it. A deleted key shows only with `with_deleted`, as always, with `deleted_at`.
  Combines with prefixes and stale (all filters are AND). Not RFC3339 and not a span, or a span that is zero, negative or over the bound: refused. A time in the future gives an empty list.
  A server older than this version ignores both fields and returns everything: restart the server with the binary.
- Without them the answer is byte for byte today's.

## 3. Long notes: `body`

- `kv_write body: {key: text}`: the key must be in `set` and the head (`set` value, <= 200 B soft as today) stays required: the head is what a reader and the search see.
  Body: at most 8192 bytes, not empty, valid UTF-8, newlines allowed. Over: the whole write is refused.
- A write replaces the whole note: a rewrite of a key without `body` has no body. The old row, body included, stays in the history (`kv_read at`).
- `kv_read`: `has_body: true` on a key that has one; the `body` only with `full: 1` (the database is asked for the body only then).
- `kv_search` indexes the HEAD only. The vector is made from `key: value`; an 8 KiB body would dilute it and can pass the embedder's window, and the head is the pointer
  that says what the note is. Search hits carry `has_body`; read the key with `full: 1` for the body.
- Migration: additive, at start: `kv.body` and `kv.expires` (TEXT NOT NULL DEFAULT ''), through the migration table (`addedColumns`) that main already runs at open, only if missing. An OLDER binary on a newer
  database keeps working: its inserts name their columns, so `body` and `project` take the defaults; it reads the head and never shows a body; a note it rewrites loses its body
  (the old row keeps it). It does not know the new arguments: its MCP refuses them as unknown arguments.

## 4. chat_search `until`, `role`

- `until`: pairs with `since` (same formats); exclusive, so `since` and `until` tile: [since, until). A date alone is the start of that day in the SERVER's time zone (the daemon's `time.Local`, as `since` always was). A fraction of a second rounds up to the stored second. `since` >= `until`: refused.
- `role: user|assistant|any` (default any; anything else refused). `user`: the query has to match M's words (the `words` column, full-text over that column only, and a
  semantic candidate is kept only if it matches there too). `assistant`: the same over the answer (the assistant's text, not tool output).
  Invariant: every hit with a role has a query word in that role's column. A purely semantic paraphrase that shares no word with it is not found under a role.
  A role with a query that has no words is refused.
- Limits, written down: (1) the semantic list is the 500 nearest, filtered after: under a role, a match beyond them is found only by the full-text list. (2) The two reads
  (candidates, then the role check) are separate statements: an ingest between them can change one exchange's words; the next search is right. (3) The answer carries `role`
  and `until` as applied: a client that sent them and does not see them in the answer talks to an older server, which ignores both: restart the server with the binary.
- Security review (an independent attacker pass, 2026-11-12): a query is its first 64 distinct words (the match cost grew faster than the input: 100000 repeats took 19 s); `/kv/write` reads at most 4 MiB; an unknown `/kv/write` field that could be a guard in another spelling (`if-absent`, `IfValue`, `absent`; names are read without regard to case) is refused like `if_*`.

## 5. Descriptions <= 1500 characters

A test asserts it for every tool description and for the server instructions (`mcpserver.Core`). What does not fit moves to `chatlog instructions`, which the descriptions point to.
