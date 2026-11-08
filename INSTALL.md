# Installing chatlog (for an LLM agent to follow)

```
CHATLOG AND ITS MCP ARE A FIRST CLASS TOOL. LOSING CONTEXT IS EXPENSIVE. A TOOL CALL IS CHEAPER THAN LOST CONTEXT.
SEARCH AND WRITE THE HISTORY BEFORE YOU GUESS, AND KEEP STATE IN IT AS YOU GO.
  - Recall before answering: chat_search with the user's own words (kv_search for your notes), then say the hit's id or "nothing".
  - Write state as it happens: kv_write what you are doing, what is done and what you learned, in the same turn.
  - A hit is what was said then, not the state now: check it against the code before relying on it.
```

You are installing chatlog for the person you work with: a local, searchable record of their Claude Code sessions,
wired in as hooks, an MCP server and a skill. Follow the steps in order; run each command, read its output, and stop and
tell the person at the first step whose check fails. Do not edit Claude Code's settings files by hand: `chatlog install`
does it, with backups. Never use sudo. How chatlog is meant to be used once installed: `docs/playbook.md`.

## 1. Check first

    uname -sm                    # e.g. "Linux x86_64" or "Darwin arm64": Linux and macOS, arm64 or x86_64; not Windows
    for c in brew go curl; do command -v "$c" || echo "$c: not found"; done   # which install path is open
    echo "$CLAUDE_CONFIG_DIR"    # if set, Claude Code keeps its files there instead of ~/.claude and ~/.claude.json
    ls -la ~/.claude/settings.json ~/.claude.json 2>&1   # may not exist yet: install creates them

Tell the person in one line: chatlog will add 8 hooks to their Claude Code settings, one MCP server named `chatlog` and
one skill, store data in `~/.local/share/chatlog`, and read the transcripts already in `~/.claude/projects` (all of
them, unless `since` is set in the config).

## 2. Pick the path

Prefer, in this order:
1. **Homebrew**, when `brew` exists: updates come with `brew upgrade`.
2. **The release binary** (`install.sh` piped from GitHub), when there is no brew: no Go needed, the tarball is checked
   against the release's SHA256SUMS. If the download fails (no release published yet, no network to GitHub), use 3.
3. **A checkout built with Go**, when the person develops chatlog or no release fits the machine. It needs Go at the
   version on the `go` line of `go.mod` or newer (`grep '^go ' go.mod` in the checkout shows it, `go version` the one
   installed; Go comes from https://go.dev/dl/ or the system's
   package manager). `install.sh` may be run from any directory: it builds in a temporary directory and writes
   nothing into the checkout or the current directory.

The container image (`ghcr.io/atvirokodosprendimai/chatlog`) runs the MCP server only; it does not install hooks. Use it
only when the person asks for no local binary.

## 3. See what would change

    # Homebrew: brew install atvirokodosprendimai/chatlog/chatlog, then
    chatlog install --dry-run
    # release binary:
    curl -fsSL https://raw.githubusercontent.com/atvirokodosprendimai/chatlog/main/install.sh | sh -s -- --dry-run
    # checkout:
    ./install.sh --dry-run            # or /path/to/checkout/install.sh --dry-run from anywhere

Expect lines naming `settings.json`, `.claude.json` and `SKILL.md` with "would change" or "would write" (or "unchanged"
on a re-install), then "would then run: chatlog ingest --all; chatlog doctor". If it says a file "does not parse", stop: show the person the error; the file must be fixed or moved
away first. The merged files are rewritten with two-space indentation (as Claude Code writes them); their content and
key order are kept.

## 4. Install

    # Homebrew:
    chatlog install && chatlog ingest --all && chatlog doctor
    # release binary:
    curl -fsSL https://raw.githubusercontent.com/atvirokodosprendimai/chatlog/main/install.sh | sh
    # checkout:
    ./install.sh

The scripts put the binary in `~/.local/bin` (`CHATLOG_BIN_DIR` overrides), run `chatlog install` (hooks, MCP server,
skill, starter notes), then `chatlog ingest --all` (reads the existing transcripts once; on a long history this takes
minutes and prints a line when done), then `chatlog doctor`. Note the backup file names install prints
(`*.chatlog-bak-<time>`); a file that did not exist before gets no backup. Nothing changes PATH: if `~/.local/bin` is
not on it, the person adds it (the hooks and the MCP entry use the full path, so chatlog works either way).

## 5. Verify

    chatlog doctor        # or ~/.local/bin/chatlog doctor

Every line must start with `ok` or `note`. `note binary on PATH`: `~/.local/bin` is not on PATH; tell the person (the
hooks use the full path and work anyway). `note server: not running`: normal before the next Claude Code start.
`note search: text only`: no embedder is set (see 7). `note history: N transcripts not fully read`: run
`chatlog ingest --all`. Any `FAIL`: stop and report the line.

Then tell the person to restart Claude Code. After the restart, in the new session:
- the session starts with a context block opening `CHATLOG AND ITS MCP ARE A FIRST CLASS TOOL`;
- each prompt carries a line starting `now ... UTC`;
- `/mcp` lists `chatlog` as connected; `/skills` lists `chatlog`;
- `chatlog events --limit 5` shows the new session's rows.

## 6. What restores after a compaction

Nothing has to be pasted or loaded by hand. When Claude Code compacts the conversation, the `SessionStart` hook (source
`compact`) first pushes the transcript into the history, then injects the digest `chatlog wake --kind compact` prints:

- the first-class rule above;
- the session's notes under `now/ waits/ task/open/ day/ thread/ rule/ known/ trigger/ tool/ fact/` (stale ones marked);
- the last 12 exchanges of the project, oldest first, each as the user's words, every tool call with its start time,
  duration, error mark and input head, and the answer, with `chat_get id=N` to fetch any one whole;
- `chat_tail project=<p> before=<id>` to page further back, and `chat_search` / `kv_search` for the rest.

The digest is capped at `hooks.wake_bytes` (24000 bytes, about 6k tokens). After `/clear` the digest carries the notes
only; a new or resumed session gets the notes and the last 5 exchanges. Run `chatlog wake --kind compact` in a shell to
see exactly what a session will get.

## 7. Smoke test without Claude Code

To see the whole path work before restarting Claude Code (or on a machine without it), feed chatlog one synthetic
turn the way Claude Code would, then find it:

    mkdir -p ~/.claude/projects/-tmp-smoke
    T=~/.claude/projects/-tmp-smoke/smoke.jsonl
    printf '%s\n' '{"type":"user","uuid":"smoke-u1","sessionId":"smoke","cwd":"/tmp/smoke","timestamp":"2026-01-01T00:00:00Z","origin":{"kind":"human"},"message":{"content":"where is the smoke test"}}' > "$T"
    printf '%s\n' '{"type":"assistant","uuid":"smoke-a1","sessionId":"smoke","timestamp":"2026-01-01T00:00:05Z","message":{"content":[{"type":"text","text":"right here"}]}}' >> "$T"
    printf '{"session_id":"smoke","cwd":"/tmp/smoke","transcript_path":"%s"}\n' "$T" | chatlog hook Stop
    chatlog events --limit 1          # a Stop row for session smoke
    chatlog server >/dev/null 2>&1 &  # search needs the server; Claude Code starts it by itself through chatlog mcp
    sleep 2                           # its first scan; output goes nowhere so the shell does not wait on it
    chatlog search smoke              # 1 hit: "User: where is the smoke test" / "A: right here"
    chatlog server --stop

Without an embedder the search also prints `DEGRADED vector: embedder not configured` and `DEGRADED rerank: reranker
not configured`: expected, the hit comes from full-text search. Times are shown in the machine's local zone.
`chatlog hook` prints nothing for a Stop and always exits 0; if a step seems to do nothing, read `hooks.log` in the
data dir (`~/.local/share/chatlog`). Remove the test session afterwards with `chatlog forget --session smoke` (the
server must be stopped) and `rm -r ~/.claude/projects/-tmp-smoke`.

## 8. Optional

- Search by meaning needs a TEI embedder: set `"tei"` (and `"reranker"`) in `~/.config/chatlog/config.json`; without it
  search is full-text only and says so in `degraded`.
- Turn pieces off with `"hooks": {"off": [...]}` in the same file (README.md, Configuration).

## Roll back

    chatlog install --uninstall       # or ./install.sh --uninstall, or the same piped from GitHub with -s -- --uninstall

This takes out only chatlog's hooks, its MCP entry and its skill; everything else in the files stays. To restore a file
exactly as it was before the first install, copy its `*.chatlog-bak-<time>` backup over it. The binary and the data dir
are kept: `brew uninstall chatlog` or delete `~/.local/bin/chatlog`; the data dir `~/.local/share/chatlog` holds the
person's history: ask before deleting it. `chatlog server --stop` ends a running server.
