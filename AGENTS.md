# AGENTS.md: how an agent organises and searches memory

An agent working with chatlog has two memories, each with one job. Put a fact in the one that owns it and point to it from the other.

| what | where | how to read | how to write |
|---|---|---|---|
| what was said and done, verbatim (the record) | chatlog `chat_*` | `chat_search` (meaning + exact words), `chat_get id`, `chat_tail` | written by the server from transcripts; never by hand |
| the session's own notes: state, rules, decisions with their why, lessons, how a tool is used | chatlog `kv_*` | `kv_read prefixes`, `kv_search`, `kv_read follow=1` for edges | `kv_write` one line <= 200 bytes, a pointer to the payload; `kv_link` relates notes |
| one project's progress: what was done, how it really works, where work stopped, its mistakes | the project's md tree (below) | headers first, then one leaf whole | in the same commit as the change |

The general knowledge (viewpoint, decisions, know-how, tasks and queues) lives in chatlog. A project's own progress lives in its tree;
chatlog keeps at most a pointer to the tree's root. A task outside the current session's work writes to and reads from ITS project's tree.

## The md tree: a key is a path, a value is a file
```
<repo>/README.md             the root: one paragraph, then `## Index` (header lines of the top children)
<repo>/docs/index.md         header lines only, one per child: the routing table of this node
<repo>/docs/<key>.md         a leaf: the payload (line 1 a `#` heading, line 2 `Up: index.md`)
<repo>/docs/<area>/index.md  a subtree of the same shape
```
A repo holding several projects keeps one tree per project folder (`<repo>/<project>/README.md` + `<project>/docs/...`).

### The header line (every Index line)
```
- <dst> | <src> | <date> | ttl=<N> | ptr=<WHERE> | <hint>   [k=v ...]
```
- `dst`: the path (a folder ends in `/`).
- `src`: where the truth is: `code@sha`, `adr:NNNN`, `session`, `prod read`, a person's role.
- `date`: when the header was last true (YYYY-MM-DD).
- `ttl`: hops left, the root's children 4, one less per index.md hop; 0 = the tree is too deep.
- `ptr`: where the payload lives (a file relative to this index, `file:line`, `chat:<id>`).
- `hint`: what the leaf holds AND when to open it, enough to decide without opening.
- `size=<lines>` and other `k=v` after the hint are optional; a reader ignores what it does not know.
Header lines live only in `index.md` files and a README's `## Index`, and always carry ` | `.

## Reading (search on headers, then the whole payload)
1. One call reads every header of a repo:
   `mrw read --grep '^- \S+ \| ' $(find . -path '*/docs/*' -name index.md -not -path './.git/*') README.md`
   (without mrw: `grep -n '^- [^ ]* | '` on the same files).
2. Pick by the headers alone; open a fitting leaf WHOLE, never a guessed slice.
3. A header that misleads is a bug: fix its words in the same turn.
4. A header's date is when it was true. Live state (agents, runs, deployments) older than an hour is a pointer, not a fact:
   check the source it names before acting.
5. In a new session open `docs/open.md` first: it says where work stopped and what is next.

## Writing (every change updates the tree in the same commit)
- New knowledge goes into the fitting leaf; a new leaf only for a new topic, then its header line in the parent `index.md`.
- After a change, rewrite the leaf and its header's `date=` and `size=`.
- How a thing works = the command that was run and worked, with its date. A comment in a file can be wrong; a run cannot.
- Where work stopped: one `docs/open.md` per project: what runs, where its output lies, what waits on a person, and an explicit `Next:`.
  Name things by their full id.
- Mistakes first: `docs/mistakes.md`, newest first, one entry per mistake: what broke and who felt it, the cause, the rule
  that would have stopped it, and where that rule lives now (a tool or a check that refuses the step, or "note only").
  Read its headings before a step of the same kind.
- A leaf over ~60 lines splits into a subtree (`docs/<area>/index.md` + leaves).
- Never live secrets; `.env` values by key names only. Never quote a person in a file that leaves the machine: write the intent in plain words.

## kv notes, in short
- Prefixes: `now/` state (old after 12 h), `waits/` what waits on a person, `task/open/` and `task/done/` the queue, `rule/` rules,
  `fact/` checked facts, `known/` bugs and gaps, `lesson/` mistakes and their rule, `tool/` how a tool is used.
- A value is a head and a pointer (`chat:<id>`, `file:line`, `repo@sha`, a URL), never the payload. A long text goes in `body`.
- Every new `rule/` or `fact/` gets a `kv_link` to what it affects or supersedes; never write "see other key" inside a value.
- Before answering what was decided: `chat_search` the person's own words, then `kv_search` the subject, then check the code.
  A hit is what was said then, not the state now.

The day-to-day use of chatlog itself (install, hooks, wake) is `docs/playbook.md`.
