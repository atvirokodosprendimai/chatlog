# chatlog

```
CHATLOG AND ITS MCP ARE A FIRST CLASS TOOL. LOSING CONTEXT IS EXPENSIVE. A TOOL CALL IS CHEAPER THAN LOST CONTEXT.
SEARCH AND WRITE THE HISTORY BEFORE YOU GUESS, AND KEEP STATE IN IT AS YOU GO.
  - Recall before answering: chat_search with the user's own words (kv_search for your notes), then say the hit's id or "nothing".
  - Write state as it happens: kv_write what you are doing, what is done and what you learned, in the same turn.
  - A hit is what was said then, not the state now: check it against the code before relying on it.
```

The record of what you and Claude Code said and did, kept on your machine, searchable by exact words and (with an
embedder) by meaning, and recalled by Claude itself through MCP: what was decided last week, why a file looks the way it
does, what the session was doing before a compaction. After a compaction, a `/clear` or a new session, the previous turns
come back automatically, with their tool calls.

Licence: AGPL-3.0-only (see `LICENSE` and `NOTICE`). Copyright (C) 2026 Atviro kodo sprendimai.

## Install

Pick one; then restart Claude Code.

    # Homebrew (macOS, Linux)
    brew tap atvirokodosprendimai/chatlog https://github.com/atvirokodosprendimai/chatlog && brew install chatlog
    chatlog install --bin "$(brew --prefix)/bin/chatlog" && chatlog doctor

    # the release binary, no Go needed (macOS arm64/Intel, Linux amd64/arm64)
    curl -fsSL https://raw.githubusercontent.com/atvirokodosprendimai/chatlog/main/install.sh | sh

    # from a checkout, built with Go
    git clone https://github.com/atvirokodosprendimai/chatlog && cd chatlog && ./install.sh

Or tell Claude Code: "read INSTALL.md in the chatlog repository and follow it". `chatlog install` puts 8 hooks into
`~/.claude/settings.json`, the MCP server into `~/.claude.json`, the skill into `~/.claude/skills/chatlog/` (each file
backed up first, other entries untouched, `$CLAUDE_CONFIG_DIR` honoured) and the starter notes into the history; the
scripts also read the history that already exists (`chatlog ingest --all`) and run `chatlog doctor`.
`--dry-run` shows what would change; `chatlog install --uninstall` takes it all out again.

A container image exists for running the MCP server without installing anything:

    docker run -i --rm --user "$(id -u):$(id -g)" -e HOME=/h -v ~/.local/share/chatlog:/data \
      -v ~/.claude/projects:/h/.claude/projects:ro ghcr.io/atvirokodosprendimai/chatlog mcp

## How it works

- **Hooks** (`chatlog hook <event>`, see `docs/hooks.md`): every prompt, tool call and session boundary is written to
  the database as it happens; each turn's transcript is pushed into the history when the turn ends, each tool call with
  its start, duration and error. The prompt gets the time (UTC, local, since the previous prompt) and the rule's first
  line; each tool result gets when the call started and how long it took. The hooks write SQLite themselves: no server
  needs to run, and a failing hook exits 0 and logs to `hooks.log`, so it never breaks a session.
- **Wake** (`chatlog wake --kind compact|clear|start`): at session start the hook injects a bounded digest (24000 bytes
  by default): the rule above, your notes, the latest exchanges with their tool calls, and the exact calls that fetch
  more. A compaction gets the most, a `/clear` only the notes.
- **Server** (`chatlog server`): keeps the database current from every transcript (a scan every 30 s), makes vectors
  when an embedder is configured, and answers searches on a unix socket (mode 0600). `chatlog mcp` starts it when
  nothing answers and it exits after 2 idle hours, so there is no service to install (`CHATLOG_AUTOSTART=0` turns
  that off; `chatlog server --stop` stops it).
- **MCP** (`chatlog mcp`): `chat_search`, `chat_get`, `chat_tail`, `chat_status` over the history; `kv_read`,
  `kv_write`, `kv_search`, `kv_link` for the session's own short notes. At initialize it sends the rules of use
  (`chatlog instructions --core`).
- **Skill** (`~/.claude/skills/chatlog/SKILL.md`): loads on "recall", "what did we decide", "where did we stop", after a
  compaction or a lost context, and says which tool answers which question.

One exchange = your message, then everything the assistant did (text, tool calls with output) up to its answer.
Project = the base name of the directory the session ran in.

Origin: the exchange typed into a subagent's window is `subagent`, the rest `main` (the user's own session). `chat_search`, `chat_tail` and the wake digest read `main` only, so what agents did never fills the user's context; `origin=subagent` or `all` widens a search or a tail.

**How it is meant to be used** (stores, note keys, value format, tasks as steps, waking up): `docs/playbook.md`.

## Recall by hand

    chatlog search "why did we drop the cache" [--since 2026-11-01] [--repo webshop] [--limit 10]
    chatlog wake --kind compact          # what a session sees after a compaction
    chatlog events --limit 20            # the latest hook records: prompts, tool calls with durations
    chatlog status

A hit is what was said then, not the state now: check it against the code.

## Search quality

Full-text search (SQLite FTS5, words weighted over the answer over tool output) works with nothing else. For search by
meaning, run a [TEI](https://github.com/huggingface/text-embeddings-inference) embedder (e.g. bge-m3) and reranker and
set them in `config.json`. Vector and full-text lists are fused by reciprocal rank and the top 50 reranked; a step that
does not run is named in `degraded`. `chatlog eval FILE` scores the variants on your own questions
(`[{"q": "...", "expect": ["<uuid>"]}]`).

## Configuration

`${XDG_CONFIG_HOME:-~/.config}/chatlog/config.json` (or `$CHATLOG_CONFIG`), every key optional; unknown keys are refused
(install, uninstall and doctor still run on defaults, with a warning, so a broken file can be repaired):

```json
{
  "sources": ["~/.claude/projects"],
  "since": "2026-01-01",
  "tei": "http://127.0.0.1:8080",
  "reranker": "http://127.0.0.1:8081",
  "timezone": "Europe/Paris",
  "hooks": {"off": ["time", "tools", "ingest", "recall"], "max_field_bytes": 2000, "wake_bytes": 24000, "context_window": 200000, "sleep": 0.6}
}
```

`since` empty (the default) reads the whole existing history on the first `ingest --all`. `hooks.off` turns pieces off:
`time` (the injected clock), `tools` (tool call records), `ingest` (the transcript pushed at turn end), `recall` (the
session-start digest), and the knowledge pieces `notes`, `prompt`, `karma`, `check`, `context` (below). `hooks.context_window`
(tokens of your model's context, default 200000) and `hooks.sleep` (the share of it at which the meter says to sleep,
default 0.6) tune the `context` piece. Environment: `CHATLOG_DIR` (data dir), `CHATLOG_SOCKET`, `CHATLOG_CONFIG`,
`CHATLOG_AUTOSTART=0`.

## Knowledge in kv

The hooks apply rules you keep in the notes; a new rule is a `kv_write`, not a code change. Key shapes, one example each
(the long form is docs/playbook.md, "Knowledge in kv"; `chatlog seed` writes a starter of each):

| Key | Example value | Does |
|---|---|---|
| `hook/prompt/01` | `use the file tool, not cat, for every file read` | printed with every prompt |
| `sit/ask-first` | `a force push \| stop and ask first` | the situation: what \| move |
| `sit/ask-first/words/1` | `force.?push` | a prompt that matches brings the situation |
| `sit/ask-first/sig/1` | `Bash :: git +push .*--force` | a tool call that matches (`TOOLRX :: INRX`) brings it, before the call; keys linked to `sit/ask-first` (`kv_link ... applies_to`) come with it, `lib/<n>/*` once a session |
| `hook/check/rule/01` | `Bash :: ^cat\s+\S+\.go :: file read through the shell` | the turn that ends with such a call is told so at the next prompt |

Also built in: a state change with no `kv_write`, and a short prompt answered with no `chat_search`/`kv_search`/`kv_read`,
are named at the next prompt; every prompt carries a context meter and, past `hooks.sleep`, `SLEEP NOW`. A situation is a
cause and its consequence, not a list of faults: it is whatever you want to happen when that prompt or tool call does.

## Privacy

- **Where**: `${XDG_DATA_HOME:-~/.local/share}/chatlog` (or `$CHATLOG_DIR`), mode 0700: `chatlog.db` (SQLite: exchanges
  with their whole text, hook records, notes), `vectors/`, `kv-vectors/`, `hooks.log`, `server.log`, a `spool.jsonl` only
  while the database was locked. The socket is under `$XDG_RUNTIME_DIR` or a private per-user temp dir. Nothing leaves
  the machine, except the texts sent to an embedder and reranker you configure.
- **What**: your prompts and the assistant's answers and tool calls as Claude Code's transcripts hold them (thinking is
  not kept); per tool call its name, start, duration and the first `max_field_bytes` of its input and output.
- **Redaction**: best-effort, by shape, before anything is stored: private keys, API keys and tokens of known formats,
  JWTs, `Authorization`, `Cookie` and `X-Api-Key` header values, `password=`, `token:`, `pass=`, `auth:`, `session=`
  style pairs, URL passwords. A tool call that touches a dotenv file (`.env`, `.env.*`, `.envrc`, `*.env`) keeps only
  the file's name. A password typed as plain prose passes.
- **Forget**: `chatlog forget --session ID` or `--before DATE` deletes exchanges, hook records and vectors (stop the
  server first). Claude Code's own transcripts (`~/.claude/projects`) are not chatlog's: delete them there.
- **Export**: `chatlog export [--session ID] > history.jsonl`.
- **Everything**: delete the data dir.

## Commands

    chatlog install [--dry-run] [--uninstall]   wire into Claude Code / take out
    chatlog doctor                              check the install
    chatlog hook EVENT                          what the hooks run (stdin: the event's JSON)
    chatlog wake --kind compact|clear|start     the session-start digest
    chatlog seed [--dry-run]                    the starter notes, where absent
    chatlog ingest --all | FILE...              read existing transcripts once, from where each was left
    chatlog server [--once] [--idle D] [--stop] ingest, embed, serve the socket
    chatlog mcp                                 the stdio MCP server
    chatlog search QUERY | status | events | export | forget | instructions [--core] | eval FILE

## Development

`go test ./...` (the install tests run the built binary in a throwaway HOME; `words_test.go` keeps one organisation's
name out of the tree), `B=<binary> sh test/spec_test.sh` (black-box tests written from the spec), `sh
scripts/release-build.sh vX.Y.Z` (the release tarballs and `SHA256SUMS`, CGO off), `packaging/homebrew/` (the
generator of `Formula/chatlog.rb`: this repository is its own tap). CI: `.github/workflows/`.
