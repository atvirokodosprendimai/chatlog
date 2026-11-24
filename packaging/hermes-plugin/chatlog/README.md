# chatlog memory provider for Hermes

Structural recall from the local chatlog archive (upstream issue #3). The MCP
server is best-effort: the agent must decide to call `chat_search`. This provider
makes recall structural — every turn, the top matching excerpts of past
conversations are injected into context with no tool call.

- **Local only.** HTTP over chatlog's unix socket. No network, no cloud, no
  credentials. `is_available()` checks binary + data dir + a socket that answers.
- **Honest recall.** A hit is what was said then, not the state now. Every hit
  carries its id and is marked an excerpt (`chat_get(id=N)` reads the whole
  exchange); stored chunks may be cut, so a hit is never presented as complete
  evidence. When retrieval degrades (e.g. the embedder is down), the block says so.
- **Writes through the existing path.** Turns and built-in memory writes land as
  chatlog notes via `kv_write` — no second store, no re-ingest of transcripts.
- **Failure is visible.** A dead server mid-session injects a marked
  "recall unavailable" note, never a silent empty block.
- **Multiplex-safe.** One instance per profile load; all state per-instance.
  Secondary agents recall read-only; only the primary writes.

The chatlog MCP tools stay mounted for deliberate queries; this provider adds
the automatic path and exposes no duplicate tools.

## Install

```sh
cp -r packaging/hermes-plugin/chatlog "$HERMES_HOME/plugins/chatlog"
# then either:
hermes memory setup            # pick chatlog
# or set in config.yaml:
#   memory:
#     provider: chatlog
```

`$HERMES_HOME` is the profile dir (e.g. `~/.hermes/profiles/<name>`). The chatlog
server must be running (`chatlog server`); data dir defaults to
`~/.local/share/chatlog`.

## Configuration

| Setting | Source (first wins) | Default |
|---|---|---|
| data dir | `CHATLOG_DIR` env | `~/.local/share/chatlog` |
| socket | `CHATLOG_SOCKET` env | derived from data dir (as the CLI does) |
| project | `CHATLOG_PROJECT` env → `$HERMES_HOME/chatlog.json` | `<profile>-fleet` |

The project isolates this profile's notes from other profiles' and from the
fleet's shared notes. Recall searches all transcripts; notes are searched within
the project.

## Verify

```sh
hermes memory status           # chatlog installed ✓ available ✓
python3 packaging/hermes-plugin/chatlog/test_provider.py   # live-socket checks
```

Acceptance (issue #3): a new session gets recall with no tool call; status
numbers do not move from re-ingest; failure degrades visibly; truncation marked.

## Backup

`backup_paths()` returns the chatlog data dir, so `hermes backup` captures the
archive together with the profile.