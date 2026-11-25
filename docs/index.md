# chatlog docs

Up: ../README.md

The project's own memory and its design notes: one file a topic, headers here. How an agent organises memory: ../AGENTS.md.

## Index

- open.md | session | 2026-11-25 | ttl=3 | ptr=open.md | where work stopped: open PRs and issues (PR #4 review verdict), local branches not on main, what is installed, Next; open first in a new session size=36
- mistakes.md | session | 2026-11-25 | ttl=3 | ptr=mistakes.md | what went wrong, why, the rule and where it lives now; read before a step of the same kind size=7
- playbook.md | code | 2026-11-25 | ttl=3 | ptr=playbook.md | day-to-day use: the rule every session carries, what goes where, how notes are written, how a session wakes; docs_test.go checks README and INSTALL point here size=199
- hooks.md | code | 2026-11-25 | ttl=3 | ptr=hooks.md | what `chatlog install` wires into Claude Code settings.json: one command per event and what each prints size=71
- wake.md | code | 2026-11-25 | ttl=3 | ptr=wake.md | the wake after a compaction or /clear: the whole load is owed until done, the gate and its list size=97
- wake-list.txt | code | 2026-11-25 | ttl=3 | ptr=wake-list.txt | one machine's wake list (files, kv prefixes, steps), loaded by `chatlog wake load` size=26
- kv-cas.md | code | 2026-11-12 | ttl=3 | ptr=kv-cas.md | design of conditional kv writes (if_absent, if_value), long notes, change feeds, search filters size=61
