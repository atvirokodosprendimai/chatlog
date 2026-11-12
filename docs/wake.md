# Wake: the whole load, owed until done

M, 2026-11-13: "kai parasau \"wakeup\" tu tada jau rimtai uzsiloadintum visus rule ir t.t. o ne partial"; "kas butu jeigu
pazadintu crontab? ne zodis wakeup mano prompt. irgi uzsiloadintum viska?"; "o wakeup.sh gal nori integruoti irgi i
chatlog? kad butu keli hook ir instaliavus naujam kompe viskas taip pat veikia?"; "o tu kai loadini wakeup memory, gali
concurrent visus irankius pakviest? pasiplanavus? kad greiciau uzsiloadint".

## Words

- **wake**: the session loads, whole and not in part, everything its owner listed: files, kv prefixes, a few free steps.
- **event situation**: a situation (`sit/<g>`) with `sit/<g>/on/<n>`, a regex that must match the whole SessionStart
  source (`startup`, `resume`, `clear` or `compact`); `startup|compact|clear` is a wake. Without any `on/` key a
  situation is what it was: it fires per match and is not sticky.
- **armed**: a situation whose event fired for this project and which no one has finished. The hook writes
  `sit/<g>/armed` (value: the source and the time) into the project's notes when the event fires.
- **done**: a write of `sit/<g>/done` (value: the LOADED line) newer than the last `sit/<g>/armed`. Armed minus done is
  what is owed. A second event writes `armed` again, so it is owed again.
- **reminder**: while armed, every UserPromptSubmit, whoever sent the prompt (the user, a cron tick), carries one short
  line that names the group, the command `chatlog wake --dump DIR` and the key to write; and the situation line
  (`sit/<g>` what | move, its linked rules) as for a prompt that matched.
- **wake list**: in the global notes, `global/wake/file/<n>` a path (`~`, `$HOME`, `$PWD` expand; a file that is not there
  is printed MISSING), `global/wake/prefix/<n>` a kv prefix to read with full 1, `global/wake/step/<n>` a free line
  (a skill to call, chat_tail). Global, so every project and every machine that shares the notes has it.
- **dump**: `chatlog wake --dump DIR` writes the files and every listed prefix, whole with bodies, into
  `DIR/page-NN.txt`, each at most 24000 bytes, cut at line ends, and prints the pages and the LOADED counts. The session
  reads every page in one turn, in parallel: that is the speed.

## Why

A session that loads only what a search finds loses its rules after a compaction. A reminder that only a typed word
(`wakeup`) can start does not start from a cron tick or a new window. And a script outside chatlog (`wakeup.sh`,
`wake-gate.sh`) is not on a new machine. The state is two keys and a list in the notes, so the notes (already shared,
searchable, versioned) are the only place a new machine needs.

## Invariants

1. A hook exits 0 and stays inside its timeout (10 s; its own budget is 3 s). A failed arm is logged, never raised.
2. Nothing armed: the prompt's context has no wake line and no extra read (the notes under `sit/` are read already).
3. An armed reminder is at most 400 bytes per group, names at most 3 groups and then counts the rest in one more short
   line, and names `chatlog wake`. The SessionStart source it quotes is cut to 16 bytes.
4. Additive only: no schema change; a new key shape (`on/`, `armed`, `done`, `global/wake/`) that an older binary does
   not read and does not mind. A situation without `on/` behaves as before.
5. Done is the notes' own time: `done` newer than `armed` by the stamp the store gave each write; clocks of two machines
   are not compared (a clock stepped back between the two writes keeps the reminder until it catches up). A `done` in
   the project's notes and one under `global/` both count, the later of the two.
6. `chatlog wake` with a wake list prints the list; without one it prints the digest as before; `--digest`, `--kind`
   and `--bytes` ask for the digest (nothing that worked is refused). `--dump` with any of them, or with no DIR, or with
   no list, is an error that says so: it never silently prints the other thing.
7. A page is at most 24000 bytes and the pages joined are the content; the page numbers have as many digits as the count
   needs (two at least), so the names sort as the pages read. A dump dir is made 0700 when it is not there; one that is
   must be a real directory of this user, closed to others, never a link, else the dump is refused. Dumps into one dir are
   serialised by a lock in it; a page is written under a temporary name and renamed into place, then the pages of an
   earlier dump that the new one does not replace are removed (only `page-N.txt`; other files stay). The reminder's dir is
   per user and per project: `<tmp>/chatlog-wake-<uid>-<hash of the project>`. `chatlog wake --dump` lists the pages: read
   those.
8. The list is notes, and notes can be written by an agent that took a prompt injection, so the load is bounded and
   narrow. A listed file is read only when, with its links resolved, it is under the home or the working directory and
   not a secret by name (a `.ssh`, `.gnupg`, `.aws`, `.kube` or `.docker` segment, `.netrc`, a dotenv name); otherwise
   it is printed REFUSED with the reason. It must be a regular file, opened without blocking and without following a
   link, judged on the open descriptor (a FIFO or a device is NOT A FILE); at most 2 MiB each and 8 MiB together (a longer
   file is cut at a line end and says CUT, the file after the budget says OVER BUDGET); a read that fails is a READ ERROR,
   not a short file. A file listed twice, by whatever name, is read once. A path expands only `~`, `$HOME`, `$PWD` (whole
   names; `$HOMEDIR` is not `$HOME`); another `$NAME` is not expanded and so the file is MISSING. The LOADED line counts
   files read, lines, notes per prefix, files cut, and names the skipped ones.
9. `chatlog wake load FILE` replaces the list in one transaction; a file with no `file`, `prefix` or `step` line is
   refused unless `--clear`; the keys it deletes are deleted on the condition that they still hold what the load read, so a
   key written meanwhile refuses the whole write; `note` lines may write only `sit/` and `global/sit/` keys.

## A new machine

Install chatlog (`chatlog install`), then `chatlog wake load docs/wake-list.txt` (the list and the situation: edit the
paths first). Nothing else lives outside the notes.

## What is not promised (decided, not forgotten)

- The wake is owed **per project, not per session**: two sessions of one project share `sit/<g>/armed`, so the first
  `done` written after the last arm finishes it for both. A per-session state would need a session key in the notes; the
  cost of the shared one is a session that skips its load when another finished first.
- The arm is one kv write in the hook's 1 s lock wait; it is not spooled as event rows are. A lock held longer loses the
  arm for that event, and the hook logs it in `hooks.log`. The next SessionStart arms again.
- A group name is a path segment of at most about 115 bytes; a longer one is refused by the notes' own key limit.
- The text of a listed file or note is printed as it is: it is the user's own and the session is told to read it, so it
  may be read as instructions. The path rule above keeps a hostile repository's link out of the home; it does not make
  a file in the project trustworthy. A listed `$PWD/CLAUDE.md` is the same file Claude Code reads on its own.
- The prefixes are read whole with their bodies and the number of list entries is not capped; the budget bounds the files
  only. A page set in the temp dir stays there until the next dump (mode 0600 in a 0700 dir of the user).
- A project key of a non-repository directory is now its path with links resolved (`internal/project`): a hook given a cwd
  that goes through a link and the CLI that reads its own working directory used to name two projects, so a wake armed by
  the hook was not seen by `chatlog wake`. Keys already stored under the link's own spelling are not moved or merged
  (nothing is deleted); they stay readable by naming the old `path:` key. A repository is keyed by its root commit and was
  never affected.
