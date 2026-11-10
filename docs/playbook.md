# The chatlog playbook

How chatlog is used day to day by a person and a Claude Code session working together for weeks: what goes where,
how notes are written, how a session wakes after a compaction. It is reproducible: `chatlog install` puts the rule in
every session, the skill in Claude Code, and the starter notes in the store (`chatlog seed` alone does the last): the
rules of this page as `rule/chatlog-first-class`, `rule/recall-first`, `rule/hit-is-past`, `rule/kv-every-step`,
`rule/task-steps`, `rule/stale`, `rule/value-format`, `tool/kv-write`, `tool/kv-link`, `tool/recall`, `tool/wake`,
`legend/shelves`, and one example of each knowledge rule below (`hook/prompt/01`, `sit/ask-first` with its `sig` and
`words`, `hook/check/rule/01`).

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

## Projects

A project is the repository a session ran in, named by its root commit: a renamed or moved folder is the same project, and
two folders with one name are two. Every read and write by project (`chat_tail`, the wake digest, `kv_*`, the hooks' rules)
acts on the project of the session, and `kv_*` also on the notes under `global/`. Put a rule for every project under
`global/` (`global/hook/prompt/1` is a line printed in every project's prompts, and `kv_read prefixes=["hook/prompt/"]`
shows it); a rule for one project goes without the prefix. A project's own note of a key wins over a global one; deleting
it mutes the global one there. `chatlog project ls` lists the projects with their aliases, and an alias, a short sha or the
key goes wherever a project is named. A fleet agent working in `~/work/x` for the project `y` sets `CHATLOG_PROJECT=y-fleet`:
its notes stay out of `y`'s wake digest and recall, and `kv_read project=y-fleet` reads them from `y`.

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

## Knowledge in kv: rules the hooks apply

The hooks do not carry rules of their own; they apply the ones in your notes. A new rule is a `kv_write`, never a code
change, and what is written is yours: chatlog knows only the key shapes. A *situation* is a plain cause and effect, a
program: an action (a prompt, a tool call) triggers its consequence (a move and the rules written for it). It need not be a
mistake to avoid; it is anything you want to happen when that action happens.

| Key | Value | When it speaks | Piece |
|---|---|---|---|
| `hook/prompt/<n>` | one line | printed with every prompt, in key order | `notes` |
| `sit/<g>` | `what \| move` | the situation itself, printed when it fires | |
| `sit/<g>/words/<n>` | a regex over the prompt (case-insensitive) | a prompt that matches fires `sit/<g>` | `prompt` |
| `sit/<g>/sig/<n>` | `TOOLRX :: INRX` | a tool call that matches fires `sit/<g>`, before the call runs | `karma` |
| `kv_link rule/<x> applies_to sit/<g>` | | the rule's key is printed with the situation | |
| `kv_link legend/lib/<n> applies_to sit/<g>` | the notes `lib/<n>/*` | all of `lib/<n>/*` printed whole, once per session | `karma` |
| `hook/check/rule/<n>` | `TOOLRX :: INRX :: message` | the turn that ends with a matching call is told `message` at the next prompt | `check` |
| `hook/check/change/<n>` | a regex over Bash commands | a command that matches counts as a state change | `check` |
| `hook/check/bare` | a comma list: `ok, status` | short answers that need no recall | `check` |
| `hook/check/skip/<n>` | a prompt prefix | prompts that start so are not audited | `check` |

- **TOOLRX** must match the whole tool name (`Bash`, `Edit|Write`, `mcp__.*__kv_write`). **INRX** is searched,
  case-insensitively and per line (`^` and `$` are a line's start and end), in the command itself for `Bash`, in the tool
  input's JSON for any other tool. Patterns are Go's RE2: no lookahead, no backreference. One that does not compile, or a
  value in the wrong shape, is left out and named at the next prompt as `kv rules NOT read: <keys>`.
- **The prompt piece** also searches the notes by meaning with the prompt's words (needs the server's embedder): prompts of
  3 words or more, the top 3 at score 0.42 or over, `map/` left out. A stale one (`now/`, `waits/` older than 12 h) is
  flagged `check against the source, then rewrite or delete`. When the server does not answer within 1.5 s the search is
  skipped and logged in `hooks.log`; the notes and the situations never wait on it.
- **Each recognition is a row** (`chatlog events`: event `Karma`, tool, input `sit/<g>`), so a situation that never fires
  can be found and dropped.
- **The check piece** audits the turn that just ended and never blocks. Built in: a state change (`Edit`, `Write`, `MultiEdit`, `NotebookEdit`,
  `Cron*`, and the Bash commands `hook/check/change/<n>` names) with no `kv_write` in the turn; a short prompt (6 words or
  fewer, or with `;)` or `:D`) answered with tool calls but no `chat_search`, `kv_search` or `kv_read`. What it found is
  printed at the next prompt as `last turn missed: ...` and then cleared. Prompts starting `<command-message>`, `<command-name>`, `<system-reminder>` or
  `<task-notification>` are not audited.
- **The context piece** says in every prompt how much of the window is used (the last main-thread message's input, cache
  read and cache creation tokens), and from `hooks.sleep` of it (60% by default; the window is `hooks.context_window`,
  200000 by default) it says `SLEEP NOW`: kv_write the pointers before going on, while there is room to do it well.

One prompt's context comes in this order: time, `hook/prompt/*` lines, last turn's misses, the context line, the notes
found and the situations the prompt names. `chatlog seed` writes one example of each, close to these, to start from:

```
kv_write set: {
  "hook/prompt/01": "use the file tool, not cat, for every file read",
  "sit/ask-first": "a force push, a drop or an rm -rf | stop, say what it will touch, wait for the user's word",
  "sit/ask-first/sig/1": "Bash :: git +push .*(--force|-f\\b)|\\brm +-rf\\b",
  "sit/ask-first/words/1": "force.?push|\\brm +-rf\\b",
  "hook/check/rule/01": "Bash :: (^|[|;&]\\s*)(cat|head|tail)\\s+[^|;&]*\\.go\\b :: a file was read through the shell"}
kv_link add: [{s: "rule/hit-is-past", p: "applies_to", o: "sit/ask-first"}]   # its key arrives with the situation
```

Turn a piece off with `hooks.off` (`notes`, `prompt`, `karma`, `check`, `context`).

## Privacy, in one paragraph

Everything stays on the machine (`~/.local/share/chatlog`, mode 0700) except what you send to an embedder you
configure. Secrets are redacted by shape before anything is stored; a tool call that touches a dotenv file keeps no
content. `chatlog forget` and `chatlog export` exist; deleting the data dir removes it all.
