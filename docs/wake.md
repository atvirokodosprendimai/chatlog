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
3. An armed reminder is at most 400 bytes per group, at most 3 groups, and names `chatlog wake`.
4. Additive only: no schema change; a new key shape (`on/`, `armed`, `done`, `global/wake/`) that an older binary does
   not read and does not mind. A situation without `on/` behaves as before.
5. Done is the notes' own time: `done` newer than `armed` by the stamp the store gave each write; clocks of two machines
   are not compared.
6. `chatlog wake` with a wake list prints the list; without one it prints the digest as before; `--digest`, `--kind`
   and `--bytes` ask for the digest (nothing that worked is refused).
7. A page is at most 24000 bytes and the pages joined are the content; a dump dir is made 0700, is never a link, and
   must be ours, else the dump is refused; only `page-*.txt` in it are replaced.
8. A listed file is read when it is a regular file, at most 2 MiB; a longer one is cut with a note, a device or a
   directory is printed NOT A FILE. A path expands only `~`, `$HOME`, `$PWD`; an unknown `$NAME` is not expanded and so
   the file is MISSING.

## A new machine

Install chatlog (`chatlog install`), then `chatlog wake load docs/wake-list.txt` (the list and the situation: edit the
paths first). Nothing else lives outside the notes.
