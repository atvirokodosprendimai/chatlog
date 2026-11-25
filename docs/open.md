# Where work stopped

Up: index.md

State read 2026-11-25 (GitHub atvirokodosprendimai/chatlog, laptop checkout ~/code/go/teisora/chatlog).

## Open on GitHub
- PR #4 `feat/hermes-memory-provider` (outside contributor): a Hermes memory provider in `packaging/hermes-plugin/chatlog/`,
  talking to the server over the local unix socket; closes issue #3. CI green (Analyze, test ubuntu + macOS).
  Review 2026-11-25 at ae4e8d4 (go test/vet/gofmt exit 0; plugin not loaded in Hermes): MERGE AFTER fixes.
  1. plugin clips by characters (head 400, body 8000: `__init__.py:51-52,358-362`), the server limits bytes (value 500,
     body 8192: `internal/store/kv.go:32,34`): long non-ASCII turns are refused, logged at debug only (`:364`).
  2. every turn's raw text goes to kv bodies unredacted (`/kv/write` is not redacted; transcripts and hooks are).
  3. `internal/know/window.go:17` maps whole model families to a 1M window without the `[1m]` suffix: a 200k user would
     get SLEEP at 600k (unsafe direction); needs a source or the suffix/override.
  4. the branch carries 9 commits of fix/embed-batch already rebased onto main (net diff 0): squash.
  5. Go changes not in the PR text: meter window detection (f9899df), redact regex escape depth (1c13b63).
  6. smaller: test_provider.py searches the contributor's own archive and writes kv in the live store; XDG_DATA_HOME
     ignored in python (`__init__.py:40`); prefetch error text says "not answering" for any error.
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
4. docs/wake.md and docs/kv-cas.md quote a person verbatim and dated (public repo): rewrite as plain intent.
