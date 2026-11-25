# Mistakes

Up: index.md

One entry per mistake, newest first: what broke and who felt it, the cause, the rule, where the rule lives.

## 2026-11-25 main went red on TestNoForbiddenWord
- Who felt it: CI on main (test ubuntu + macOS) for commit 4a9fef5; the maintainer saw the failed job.
- Cause: docs/open.md named local paths of the maintainer's other project; this repo is standalone and words_test.go refuses
  that word anywhere. Before the push only TestDocs was run, not the whole suite.
- Rule: before any push to main run `go test ./...` whole (it takes ~15 s), never a single test; local paths of other
  projects never go into this repo's docs.
- Where it lives: words_test.go (the gate that caught it); the run-whole-suite step is a note only.
