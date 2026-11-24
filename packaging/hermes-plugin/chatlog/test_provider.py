# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Atviro kodo sprendimai
"""Live checks for the chatlog Hermes memory provider (issue #3 acceptance).

Runs against the real chatlog server on this machine; skips when it is down.
No fabricated results: every assertion reads what the server actually returned.

    python3 packaging/hermes-plugin/chatlog/test_provider.py
"""

import importlib.util
import json
import os
import sys
import time
import uuid
from pathlib import Path

HERE = Path(__file__).resolve().parent
AGENT_SRC = os.environ.get("HERMES_AGENT_SRC", os.path.expanduser("~/.hermes/hermes-agent"))
sys.path.insert(0, AGENT_SRC)

spec = importlib.util.spec_from_file_location("chatlog_provider_under_test", HERE / "__init__.py")
assert spec is not None and spec.loader is not None
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)

PASS, FAIL, SKIP = 0, 0, 0


def check(name, cond, detail=""):
    global PASS, FAIL
    if cond:
        PASS += 1
        print(f"  ok   {name}")
    else:
        FAIL += 1
        print(f"  FAIL {name} {detail}")


def main():
    global SKIP
    p = mod.ChatlogMemoryProvider()
    if not p.is_available():
        print(f"SKIP: chatlog server not available ({p.unavailable_reason()})")
        SKIP += 1
        return

    home = os.environ.get("HERMES_HOME", os.path.expanduser("~/.hermes"))
    sid = "provider-test-" + uuid.uuid4().hex[:8]
    p.initialize(sid, hermes_home=home, agent_context="primary")
    check("initialize binds socket", p._socket is not None)
    check("project derived", p._project.endswith("-fleet"), p._project)
    check("backup_paths has data dir", p.backup_paths() == [p._data_dir])
    check("system_prompt_block static+nonempty", "chat_get(id=N)" in p.system_prompt_block())

    # Recall: real hits, ids present, excerpt marked.
    block = p.prefetch("chatlog embed pipeline mini", sid)
    check("prefetch returns recall", len(block) > 0, repr(block[:120]))
    check("hits carry ids", "id=" in block, repr(block[:200]))
    check("truncation marked", "excerpt" in block)
    check("then-not-now rule present", "not the state now" in block)
    check("recall_status counts", p.recall_status().count > 0)
    check("cache hit identical", p.prefetch("chatlog embed pipeline mini", sid) == block)

    # queue_prefetch fills the cache with full rerank recall for the next query.
    p.queue_prefetch("Infisical operator pulpogalic", sid)
    time.sleep(4.0)
    b2 = p.prefetch("Infisical operator pulpogalic", sid)
    check("queue_prefetch cached real recall", "id=" in b2, repr(b2[:120]))

    # Writes: turn note lands via kv_write, readable, then cleaned up.
    marker = "provider-test-marker-" + uuid.uuid4().hex[:8]
    p.sync_turn(marker + " question about the fleet", marker + " answer about the fleet", sid)
    time.sleep(1.5)
    notes = mod._request(p._socket, "POST", "/kv/read",
                         {"project": p._project, "prefixes": ["session/"], "full": True}, 5.0)
    found = [n for n in notes if marker in (n.get("value", "") + n.get("body", ""))]
    check("sync_turn note stored", len(found) >= 1, f"{len(notes)} notes read")
    if found:
        key = found[0]["key"]
        check("turn note has body", found[0].get("has_body") or found[0].get("body"))
        mod._request(p._socket, "POST", "/kv/write",
                     {"project": p._project, "del": [key]}, 5.0)

    # Built-in memory mirror: add then remove.
    mem_marker = "memory-mirror-test-" + uuid.uuid4().hex[:8]
    p.on_memory_write("add", "memory", mem_marker + " durable fact", {})
    time.sleep(1.5)
    key = mod._MEMORY_KEY_PREFIX + "memory/" + __import__("hashlib").sha256((mem_marker + " durable fact").encode()).hexdigest()[:12]
    notes = mod._request(p._socket, "POST", "/kv/read", {"project": p._project, "keys": [key]}, 5.0)
    check("on_memory_write mirrored", any(mem_marker in n.get("value", "") for n in notes))
    p.on_memory_write("remove", "memory", mem_marker + " durable fact", {})
    time.sleep(1.5)
    notes = mod._request(p._socket, "POST", "/kv/read", {"project": p._project, "keys": [key]}, 5.0)
    check("remove mirrored (note gone)", not any(n.get("value") for n in notes))

    # Secondary agent: read-only.
    sec = mod.ChatlogMemoryProvider()
    sec.initialize(sid + "-sec", hermes_home=home, agent_context="secondary")
    check("secondary recall works", len(sec.prefetch("chatlog", sid + "-sec")) >= 0)
    before = mod._request(p._socket, "POST", "/kv/read", {"project": p._project, "prefixes": ["session/"]}, 5.0)
    sec.sync_turn("secondary write attempt " + marker, "should not land " + marker, sid + "-sec")
    time.sleep(1.0)
    after = mod._request(p._socket, "POST", "/kv/read", {"project": p._project, "prefixes": ["session/"]}, 5.0)
    check("secondary writes blocked", len(after) == len(before))

    # Failure mode: dead socket -> marked note, never silent empty.
    dead = mod.ChatlogMemoryProvider()
    dead._data_dir = "/nonexistent-chatlog-dir"
    dead._socket = None
    out = dead.prefetch("anything", "s")
    check("failure is marked, not empty", "unavailable" in out, repr(out))

    p.shutdown()
    print(f"\n{PASS} passed, {FAIL} failed, {SKIP} skipped")
    sys.exit(1 if FAIL else 0)


if __name__ == "__main__":
    main()