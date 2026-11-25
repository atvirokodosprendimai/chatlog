# Where work stopped

Up: index.md

State read 2026-11-25 (GitHub atvirokodosprendimai/chatlog, laptop checkout ~/code/go/teisora/chatlog).

## Open on GitHub
- PR #4 `feat/hermes-memory-provider` (outside contributor): a Hermes memory provider in `packaging/hermes-plugin/chatlog/`,
  talking to the server over the local unix socket; closes issue #3. CI green (Analyze, test ubuntu + macOS). Review running
  2026-11-25; verdict goes here.
- Issue #3: ship a Hermes memory provider beside the MCP server (PR #4 answers it).
- Remote branch `fix/embed-batch`: not read yet.

## Local, not on main
- `task/kv-expires` (+5 commits on main 36dc2a1): kv_search by meaning, kv_link edges and kv_read follow/linked_to/age,
  `instructions --core`, optional `expires` per key with `kv_read stale=1`, stale prefixes task/ todo/ promise/.
  Also `task/kv-cas`, `task/kv-cas-main` (if_absent / if_value). Not pushed. Which of them the installed binary was built
  from is not checked: `~/.sandboxes/teisora/bin/chatlog` has no version verb.
- `design.md` untracked in the checkout (system design write-up of 2026-11-13).

## Install (how it was done)
Build `go build -o bin/chatlog ./cmd/chatlog`, copy to `~/.sandboxes/teisora/bin/chatlog`, restart the chatlog service;
MCP clients pick up new tool fields only after a reconnect (`/mcp`).

## Next
1. Finish the PR #4 review; merge or ask for fixes; thank the contributor in the PR.
2. Decide the local kv branches: push them as a PR onto main, so main = what runs.
3. Give the binary a `version` verb (the installed build cannot be named today).
