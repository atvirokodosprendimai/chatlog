# The chatlog playbook

How chatlog is used day to day by a person and a Claude Code session working together for weeks: what goes where,
how notes are written, how a session wakes after a compaction. It is reproducible: `chatlog install` puts the rule in
every session, the skill in Claude Code, and the starter notes in the store (`chatlog seed` alone does the last): the
rules of this page as `rule/chatlog-first-class`, `rule/recall-first`, `rule/hit-is-past`, `rule/kv-every-step`,
`rule/task-steps`, `rule/stale`, `rule/value-format`, `tool/kv-write`, `tool/kv-link`, `tool/recall`, `tool/wake`,
`legend/shelves`.

```
CHATLOG AND ITS MCP ARE A FIRST CLASS TOOL. LOSING CONTEXT IS EXPENSIVE. A TOOL CALL IS CHEAPER THAN LOST CONTEXT.
SEARCH AND WRITE THE HISTORY BEFORE YOU GUESS, AND KEEP STATE IN IT AS YOU GO.
  - Recall before answering: chat_search with the user's own words (kv_search for your notes), then say the hit's id or "nothing".
  - Write state as it happens: kv_write what you are doing, what is done and what you learned, in the same turn.
  - A hit is what was said then, not the state now: check it against the code before relying on it.
```

## Why

A long collaboration loses its context in three ways: a compaction summarises the conversation and drops most of it,
a `/clear` drops all of it on purpose, and a new session starts with nothing. What the user decided, what was tried and
failed, what is half done: if it lived only in the conversation, it is gone. Re-deriving it costs more than one tool
call that reads it back, and guessing it costs trust. So the session writes as it goes and reads before it answers.

## Three stores, three jobs

| Store | Holds | Written by | Read with |
|---|---|---|---|
| **chat** (exchanges) | the verbatim record: the user's words, the assistant's answers, every tool call with its time, duration, input and output | the hooks and the server, from Claude Code's transcripts; never edited | `chat_search`, `chat_get`, `chat_tail` |
| **kv** (notes) | the session's own notes: one short line per key, a pointer into the other stores | the session, with `kv_write` and `kv_link` | `kv_read`, `kv_search` |
| **files** (CLAUDE.md and the like) | only the instructions every session must load before its first step | the user, or the session on the user's word | loaded by Claude Code |

A fact lives in the one store that owns it and is pointed to from the others. A decision is in chat (the words) and
noted in kv (the gist and `chat:<id>`); it goes into a file only if every future session must obey it before it can
search.

## Notes: keys

At most five segments split by `/`, no spaces. The first segment says the kind:

| Prefix | Kind | Lifetime |
|---|---|---|
| `now/<area>` | live state: what runs, what is half done | stale after 12 h: rewrite at each task end |
| `waits/<topic>` | what waits on the user: a question, a decision | stale after 12 h; delete when answered |
| `task/open/<name>` | a task in progress: `k/N done: next step` | updated after each step |
| `task/done/<name>` | a finished task, with its evidence (a commit, a run, a number) | kept |
| `rule/<name>` | a rule the user gave, with its date and words | until the user changes it |
| `known/<name>` | a bug, a gap, a trap found | until fixed (then `fixed_by` edge) |
| `fact/<name>` | a checked fact | until it changes |
| `tool/<name>` | how a tool is used, learned the hard way | until the tool changes |
| `day/`, `thread/` | the day's summary, a long thread's state | optional; rewrite or delete when the day or thread ends |
| `trigger/` | a condition to watch for and what to do when it happens | until it fires or is dropped |
| `legend/` | the notation the notes use | optional |

`now/` and `waits/` keys older than 12 hours come back marked `stale`: they were the state then; read the source again
before saying them. The other kinds do not go stale by age: they stand until someone changes them, and like any hit
they are checked against the code or the running system before a session acts on them.

## Notes: values

- **One line, at most 200 bytes** (kept with a warning up to 500; past 500 the whole write is refused). A note is a
  pointer, not the payload.
- **Shape**: `GIST | pointer | date`, for example `STATE release build green | repo@3f2a1c9 | 2026-11-08`.
- **Pointers**: `chat:<id>` (an exchange), `chat:<id>@<from>-<to>` (a byte range of it), `file:path:line`,
  `repo@sha:path:A-B`, a URL.
- **The user's words** go in quotes with their date, in the user's own language: `rule/tests-first: user 11-05 'pirma
  testai'`. Lexical recall needs the user's words in the note.
- **The session's own conclusions** are marked as such (`claim:` or "(my reading)"); a dated quote of the user
  outranks a claim.
- **Times** come from the clock (`date -u`, or the time the hook injected), never from a guess.

## Writing

```json
kv_write {"set": {"now/release": "STATE build green, tag pending | repo@3f2a1c9 | 2026-11-08T10:12Z",
                  "task/open/3-docs": "2/4 done: next INSTALL.md rollback section"},
          "del": ["now/release-old"]}
```

- `set` and `del` are the only arguments; there are no `key=`/`value=` parameters. One bad entry writes nothing.
- A write never changes an old row: `kv_read at=<RFC3339>` shows the notes as they stood then.
- **Write doing, done and learned at once, never hold it in context**: after every result, decision, agent start or
  end, in the same turn. Delete or supersede the key it replaces in the same call.
- **Relate with edges, not arrows in text**: `kv_link add [{"s": "waits/api-key", "p": "blocked_by", "o":
  "now/vault"}]`. Predicates: `affects`, `blocked_by`, `fixed_by`, `decided_by`, `applies_to`, `supersedes`,
  `see_also`. `kv_read follow=1` lists each key's edges both ways; `linked_to=<object>` reads everything linked to it.

## Tasks as steps

Every task the user asks for becomes `task/open/<n>-<name>` with `k/N done: next step`, updated after each step, so
anyone (the user, the next session, an agent) sees that it moves and how much is left. When it is done, one `kv_write`
sets `task/done/<n>-<name>` with the evidence and deletes the open key. `kv_read prefixes ["task/open/"]` is the queue.
A background agent gets the same convention in its brief.

## Reading: recall before answering

1. A short, unclear or corrective message from the user: `chat_search` with the user's own words, and `kv_search` on
   the subject. Say what was found (`chat:1234`, `rule/x`) or "nothing".
2. A correction: search whether the user said it before. If yes, it is a repeat: note it with the count, so the next
   session does not make the same mistake.
3. A hit is what was said then, not the state now: check it against the code, the running system or the repository
   before acting on it.
4. Read whole when the gist is not enough: `chat_get id=<id>` pages the full exchange, tool output included; a note's
   pointer leads to the source.

| Question | Call |
|---|---|
| What did the user say, when, in which words | `chat_search query=<their words> [since] [repo]` |
| One whole exchange | `chat_get id=<id> [offset] [length]` |
| What happened before a compaction or yesterday | `chat_tail project=<p> [before=<next_before>]` |
| My notes, by key | `kv_read prefixes=["now/","waits/","task/open/"]` |
| My notes, by subject | `kv_search query=<subject> [prefix]` |

## Waking up

The session-start hook injects a bounded digest (default 24000 bytes, about 6k tokens; `hooks.wake_bytes` in
`config.json`). `chatlog wake --kind <kind>` prints the same text in a shell.

| Kind | When | Holds |
|---|---|---|
| `compact` | after a compaction | the rule; notes under `now/ waits/ task/open/ day/ thread/ rule/ known/ trigger/ tool/ fact/`; the last 12 exchanges with their tool calls; the calls that fetch more |
| `clear` | after `/clear` | the rule; notes under `now/ waits/ task/open/ rule/ tool/`; no exchanges: a clear was asked for to start small |
| `start` | a new session or a resume | as `clear`, plus the last 5 exchanges |

After waking: read the digest, follow only the pointers the task needs, say in one line what it changes against the
summary, then go on. The day before is one `chat_tail` page after another, not a guess.

## An optional pattern: a library of situations

A session repeats a few situations many times: about to commit, about to run tests, about to touch production, about
to brief an agent. A small library maps each situation's signature (the tool and a few words of its input) to its
**move** (the one thing to do first) and the keys of the rules that apply. A `PreToolUse` hook can match the signature
and inject `situation: commit | move: run the gate first | rules: rule/commit rule/gate`, and the session reads those
rules by key. Keep the library in the notes (`sit/<name>` keys, linked with `applies_to` edges to their rules) so it
grows as lessons come. chatlog does not ship one: the situations and rules are each team's own.

## Privacy, in one paragraph

Everything stays on the machine (`~/.local/share/chatlog`, mode 0700) except what you send to an embedder you
configure. Secrets are redacted by shape before anything is stored; a tool call that touches a dotenv file keeps no
content. `chatlog forget` and `chatlog export` exist; deleting the data dir removes it all.
