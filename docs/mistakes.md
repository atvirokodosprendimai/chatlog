# Mistakes

Up: index.md

One entry per mistake, newest first: what broke and who felt it, the cause, the rule, where the rule lives.

## 2026-11-25 FTS search took seconds: the filter join read the row of every match
- Who felt it: every session's recall; /search mode=fts answered in 1.4-10 s on a 58k-exchange file, past a 2 s
  first-turn budget, so recall did not land.
- Cause: store.FTS joined fts to exchanges and filtered before ORDER BY bm25 ... LIMIT, so a common word (24k matches)
  read 24k rows, 5-20k preads per query; on a filesystem that caches no file data each one is a disk read.
- Rule: rank in the full-text index alone, then filter only the top candidates in rank order, batch by batch.
- Where it lives: internal/store/fts_test.go (TestFTSReadsTheRowsOfTheTopMatchesNotOfEveryMatch counts read
  syscalls; TestFTSRanksAsTheJoinDoesWhenTheFilterDropsMostMatches keeps the join's order).

## 2026-11-25 main went red on TestNoForbiddenWord
- Who felt it: CI on main (test ubuntu + macOS) for commit 4a9fef5; the maintainer saw the failed job.
- Cause: docs/open.md named local paths of the maintainer's other project; this repo is standalone and words_test.go refuses
  that word anywhere. Before the push only TestDocs was run, not the whole suite.
- Rule: before any push to main run `go test ./...` whole (it takes ~15 s), never a single test; local paths of other
  projects never go into this repo's docs.
- Where it lives: words_test.go (the gate that caught it); the run-whole-suite step is a note only.
