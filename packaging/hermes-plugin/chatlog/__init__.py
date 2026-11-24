# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Atviro kodo sprendimai
"""chatlog memory provider for Hermes Agent (issue #3).

Structural recall from the local chatlog archive: every turn, the top matching
excerpts of past conversations are injected into context without a tool call.
A hit is what was said then, not the state now; every hit carries its id and is
marked as an excerpt, so a truncated chunk can never pass as complete evidence.

Local only: the provider speaks HTTP over chatlog's unix socket. No network, no
cloud, no credentials. The chatlog MCP server (chat_search, kv_*, ...) stays
mounted for deliberate queries; this provider adds the automatic recall path,
it does not duplicate those tools.

Install: copy this directory to $HERMES_HOME/plugins/chatlog/ and set
``memory.provider: chatlog`` in config.yaml (or ``hermes memory setup``).
One instance per profile load; all state is per-instance (multiplex contract).
"""

from __future__ import annotations

import glob
import hashlib
import http.client
import json
import logging
import os
import shutil
import socket
import tempfile
import threading
import time
from pathlib import Path
from typing import Any, Dict, List, Optional

from agent.memory_provider import MemoryProvider, RecallStatus, spawn_context_thread

logger = logging.getLogger(__name__)

_DEFAULT_DATA_DIR = os.path.join(os.path.expanduser("~"), ".local", "share", "chatlog")
_SEARCH_TIMEOUT = 2.0     # sync prefetch budget: local FTS, normally milliseconds
_RERANK_TIMEOUT = 10.0    # background path: embed + rerank via the configured endpoints
_AVAIL_TIMEOUT = 0.25     # is_available connect probe: local IPC, not network
_WRITE_TIMEOUT = 5.0
_TOP_HITS = 5
_TOP_KV = 3
_WORDS_CLIP = 300
_ANSWER_CLIP = 200
_BLOCK_MAX = 2600         # chars of recall injected per turn
_KV_VALUE_CLIP = 400      # head stored per turn note (kv_search embeds "key: value")
_KV_BODY_CLIP = 8000      # server refuses bodies over 8192 bytes
_TURN_KEY_PREFIX = "session/"
_MEMORY_KEY_PREFIX = "memory/"


def _clip(text: str, n: int) -> str:
    text = " ".join((text or "").split())
    return text if len(text) <= n else text[: n - 1] + "…"


def _default_data_dir() -> str:
    return os.environ.get("CHATLOG_DIR") or _DEFAULT_DATA_DIR


def _socket_candidates(data_dir: str) -> List[str]:
    """Mirror config.Socket(): sha256(abs data dir)[:8 hex], under $XDG_RUNTIME_DIR
    or the per-user temp dir. The server may have been started with a different
    TMPDIR than this process (Hermes sets its own), so also probe the macOS
    per-user temp roots and /tmp."""
    h = hashlib.sha256(os.path.abspath(data_dir).encode()).hexdigest()[:8]
    uid = os.getuid()
    out: List[str] = []
    env_sock = os.environ.get("CHATLOG_SOCKET")
    if env_sock:
        out.append(env_sock)
    xdg = os.environ.get("XDG_RUNTIME_DIR")
    if xdg:
        out.append(os.path.join(xdg, f"chatlog-{h}.sock"))
    out.append(os.path.join(tempfile.gettempdir(), f"chatlog-{uid}", f"{h}.sock"))
    out.extend(sorted(glob.glob(f"/var/folders/*/*/T/chatlog-{uid}/{h}.sock")))
    out.append(os.path.join("/tmp", f"chatlog-{uid}", f"{h}.sock"))
    seen = set()
    return [p for p in out if not (p in seen or seen.add(p))]


def _find_socket(data_dir: str) -> Optional[str]:
    for path in _socket_candidates(data_dir):
        if os.path.exists(path):
            return path
    return None


class _UnixHTTP(http.client.HTTPConnection):
    """HTTP over a unix socket; Host header 'chatlog' as the CLI uses."""

    def __init__(self, sock_path: str, timeout: float):
        super().__init__("chatlog", timeout=timeout)
        self._sock_path = sock_path

    def connect(self) -> None:
        s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        s.settimeout(self.timeout)
        s.connect(self._sock_path)
        self.sock = s


def _request(sock_path: str, method: str, path: str, body: Optional[dict], timeout: float) -> Any:
    conn = _UnixHTTP(sock_path, timeout)
    try:
        payload = json.dumps(body).encode() if body is not None else None
        headers = {"Content-Type": "application/json"} if payload else {}
        conn.request(method, path, body=payload, headers=headers)
        resp = conn.getresponse()
        data = resp.read()
        if resp.status != 200:
            raise RuntimeError(f"chatlog {path}: HTTP {resp.status}: {data[:200]!r}")
        return json.loads(data) if data else None
    finally:
        conn.close()


class ChatlogMemoryProvider(MemoryProvider):
    """Local transcript archive as structural memory. One instance per load."""

    def __init__(self) -> None:
        self._data_dir = _default_data_dir()
        self._socket: Optional[str] = None
        self._project = ""
        self._session_id = ""
        self._agent_context = ""
        self._hermes_home = ""
        self._lock = threading.Lock()
        self._cache: Dict[str, str] = {}          # session_id -> formatted recall block
        self._cache_query: Dict[str, str] = {}
        self._inflight: set = set()
        self._turn_no: Dict[str, int] = {}
        self._last_count = 0
        self._last_chars = 0
        self._avail_cache: Optional[bool] = None
        self._avail_at = 0.0
        self._stopping = False

    # -- identity / availability -------------------------------------------

    @property
    def name(self) -> str:
        return "chatlog"

    def is_available(self) -> bool:
        """Binary + data dir + a socket that answers. Local IPC only, no network."""
        now = time.monotonic()
        if self._avail_cache is not None and now - self._avail_at < 2.0:
            return self._avail_cache
        ok = False
        try:
            binary = shutil.which("chatlog") or os.path.join(os.path.expanduser("~"), ".local", "bin", "chatlog")
            if os.path.isfile(binary) and os.path.isdir(self._data_dir):
                sock = self._socket or _find_socket(self._data_dir)
                if sock:
                    conn = _UnixHTTP(sock, _AVAIL_TIMEOUT)
                    try:
                        conn.connect()
                        ok = True
                    finally:
                        conn.close()
        except Exception:
            ok = False
        self._avail_cache, self._avail_at = ok, now
        return ok

    def unavailable_reason(self) -> str:
        if not os.path.isdir(self._data_dir):
            return f"chatlog data dir {self._data_dir} does not exist — run `chatlog install`"
        if not _find_socket(self._data_dir):
            return "chatlog server socket not found — start `chatlog server`"
        return "chatlog server does not answer on its socket — start `chatlog server`"

    def backup_paths(self) -> List[str]:
        d = self._data_dir
        return [d] if os.path.isdir(d) else []

    # -- lifecycle ----------------------------------------------------------

    def initialize(self, session_id: str, **kwargs) -> None:
        self._session_id = session_id or ""
        self._hermes_home = str(kwargs.get("hermes_home") or "")
        self._agent_context = str(kwargs.get("agent_context") or "")
        self._data_dir = _default_data_dir()
        self._socket = _find_socket(self._data_dir)
        # Project for this profile's notes: env > $HERMES_HOME/chatlog.json > <profile>-fleet.
        project = os.environ.get("CHATLOG_PROJECT", "")
        if not project and self._hermes_home:
            try:
                cfg = json.loads((Path(self._hermes_home) / "chatlog.json").read_text())
                project = str(cfg.get("project") or "")
            except Exception:
                pass
        if not project:
            profile = os.path.basename(self._hermes_home.rstrip("/")) if self._hermes_home else ""
            project = f"{profile}-fleet" if profile and profile != ".hermes" else "hermes-fleet"
        self._project = project
        self._stopping = False
        logger.info("chatlog provider: project=%s socket=%s", self._project, self._socket)

    def _writes_allowed(self) -> bool:
        # Multiplex: only the primary agent writes; secondaries recall read-only.
        return self._agent_context in ("", "primary")

    def system_prompt_block(self) -> str:
        return (
            "## chatlog recall\n"
            "This session has chatlog, a local archive of past conversations, as structural memory: "
            "relevant excerpts are injected with each turn, no tool call needed.\n"
            "- A hit is what was said then, not the state now. Verify against code and config before acting on it.\n"
            "- Hits are excerpts, never complete evidence; each carries an id. Read the whole exchange with "
            "chat_get(id=N) when it matters.\n"
            "- The chatlog MCP tools (chat_search, chat_get, kv_read, kv_search, kv_write, ...) stay available "
            "for deliberate queries and notes."
        )

    # -- recall -------------------------------------------------------------

    def _search_block(self, query: str, fast: bool = False) -> str:
        """One search round; formats the recall block. Raises on failure.

        fast=True is the sync first-turn path: local FTS only, milliseconds.
        The background path (fast=False) runs the full embed+rerank pipeline
        and adds the profile's notes; it may take seconds, off the turn path.
        """
        if not self._socket:
            self._socket = _find_socket(self._data_dir)
        if not self._socket:
            raise RuntimeError("no socket")
        timeout = _SEARCH_TIMEOUT if fast else _RERANK_TIMEOUT
        resp = _request(
            self._socket, "POST", "/search",
            {"query": query, "limit": _TOP_HITS, "role": "any", "origin": "all",
             "mode": "fts" if fast else "rerank"},
            timeout,
        )
        hits = resp.get("hits") or []
        degraded = resp.get("degraded") or []
        kv_hits: List[dict] = []
        if not fast:
            try:
                kv_hits = _request(
                    self._socket, "POST", "/kv/search",
                    {"query": query, "project": self._project, "limit": _TOP_KV},
                    timeout,
                ) or []
            except Exception:
                pass  # notes are a bonus; transcript recall stands without them
        lines: List[str] = []
        for i, h in enumerate(hits, 1):
            when = str(h.get("time", ""))[:10]
            proj = h.get("project") or "?"
            words = _clip(h.get("words", ""), _WORDS_CLIP)
            answer = _clip(h.get("answer_head", ""), _ANSWER_CLIP)
            line = f"{i}. (id={h.get('id')}, {when}, {proj}) \"{words}\""
            if answer:
                line += f" → {answer}"
            line += f" [excerpt; full: chat_get(id={h.get('id')})]"
            lines.append(line)
        for k in kv_hits[:_TOP_KV]:
            key = k.get("key", "?")
            val = _clip(k.get("value", ""), 160)
            lines.append(f"- note {key}: {val} [kv; full: kv_read]")
        if not lines:
            self._last_count, self._last_chars = 0, 0
            return ""
        header = "[chatlog recall — excerpts of past conversations; what was said then, not the state now]"
        block = header + "\n" + "\n".join(lines)
        if degraded:
            block += "\n[retrieval degraded: " + "; ".join(str(d) for d in degraded) + "]"
        if len(block) > _BLOCK_MAX:
            block = block[: _BLOCK_MAX - 1] + "… [recall truncated]"
        self._last_count = len(hits) + len(kv_hits[:_TOP_KV])
        self._last_chars = len(block)
        return block

    def prefetch(self, query: str, session_id: str = "", **kwargs) -> str:
        sid = session_id or self._session_id
        query = (query or "").strip()
        if not query:
            return ""
        with self._lock:
            if self._cache_query.get(sid) == query and sid in self._cache:
                return self._cache[sid]
        # Cache miss (first turn of a session): one bounded synchronous FTS round
        # so a new session gets recall with no tool call. Local SQLite; milliseconds.
        # The full embed+rerank recall for this query is queued for the next turn.
        try:
            block = self._search_block(query, fast=True)
        except Exception as exc:
            logger.warning("chatlog prefetch failed: %s", exc)
            self._last_count, self._last_chars = 0, 0
            return "[chatlog recall unavailable this turn: server not answering — start `chatlog server`]"
        with self._lock:
            self._cache[sid] = block
            self._cache_query[sid] = query
        self.queue_prefetch(query, sid)
        return block

    def queue_prefetch(self, query: str, session_id: str = "", **kwargs) -> None:
        sid = session_id or self._session_id
        query = (query or "").strip()
        if not query or self._stopping:
            return
        with self._lock:
            if sid in self._inflight:
                return
            self._inflight.add(sid)

        def work() -> None:
            try:
                block = self._search_block(query)
                with self._lock:
                    self._cache[sid] = block
                    self._cache_query[sid] = query
            except Exception as exc:
                logger.debug("chatlog queue_prefetch failed: %s", exc)
            finally:
                with self._lock:
                    self._inflight.discard(sid)

        spawn_context_thread(work, name="chatlog-prefetch").start()

    def recall_status(self) -> RecallStatus:
        return RecallStatus(provider_label="chatlog", count=self._last_count)

    # -- writes -------------------------------------------------------------

    def _kv_write(self, set_map: Dict[str, str], body: Optional[Dict[str, str]] = None,
                  delete: Optional[List[str]] = None) -> None:
        if not self._socket:
            self._socket = _find_socket(self._data_dir)
        if not self._socket:
            raise RuntimeError("no socket")
        req: Dict[str, Any] = {"project": self._project, "set": set_map}
        if body:
            req["body"] = body
        if delete:
            req["del"] = delete
        _request(self._socket, "POST", "/kv/write", req, _WRITE_TIMEOUT)

    def sync_turn(self, user_content: str, assistant_content: str, session_id: str = "", **kwargs) -> None:
        if not self._writes_allowed() or self._stopping:
            return
        user_content, assistant_content = user_content or "", assistant_content or ""
        if len(user_content) + len(assistant_content) < 40:
            return  # nothing worth a note
        sid = session_id or self._session_id
        with self._lock:
            n = self._turn_no.get(sid, 0) + 1
            self._turn_no[sid] = n
        key = f"{_TURN_KEY_PREFIX}{sid[:16]}/turn-{n:04d}"
        head = _clip(user_content, 180) + " → " + _clip(assistant_content, 180)
        body = ("user: " + user_content + "\nassistant: " + assistant_content)[:_KV_BODY_CLIP]

        def work() -> None:
            try:
                self._kv_write({key: head[:_KV_VALUE_CLIP]}, {key: body})
            except Exception as exc:
                logger.debug("chatlog sync_turn write failed: %s", exc)

        spawn_context_thread(work, name="chatlog-sync").start()

    def on_memory_write(self, action: str, target: str, content: str, metadata: Optional[dict] = None) -> None:
        """Mirror built-in MEMORY.md / USER.md writes into chatlog notes."""
        if not self._writes_allowed() or self._stopping or target not in ("memory", "user"):
            return
        metadata = metadata or {}

        def key_of(text: str) -> str:
            return _MEMORY_KEY_PREFIX + target + "/" + hashlib.sha256((text or "").encode()).hexdigest()[:12]

        def work() -> None:
            try:
                if action == "add":
                    self._kv_write({key_of(content): _clip(content, _KV_VALUE_CLIP)},
                                   {key_of(content): content[:_KV_BODY_CLIP]})
                elif action == "replace":
                    old = str(metadata.get("previous_content") or "")
                    self._kv_write({key_of(content): _clip(content, _KV_VALUE_CLIP)},
                                   {key_of(content): content[:_KV_BODY_CLIP]},
                                   delete=[key_of(old)] if old else None)
                elif action == "remove":
                    old = str(metadata.get("previous_content") or content)
                    self._kv_write({}, delete=[key_of(old)])
            except Exception as exc:
                logger.debug("chatlog on_memory_write failed: %s", exc)

        spawn_context_thread(work, name="chatlog-memory-mirror").start()

    # -- session boundaries ---------------------------------------------------

    def on_session_switch(self, new_session_id: str, parent_session_id: str = "",
                          reset: bool = False, **kwargs) -> None:
        with self._lock:
            if reset:
                self._cache.clear()
                self._cache_query.clear()
                self._turn_no.clear()
            self._session_id = new_session_id or ""

    def on_session_end(self, messages: List[dict]) -> None:
        pass  # turns are already persisted by sync_turn; nothing to flush

    def shutdown(self) -> None:
        self._stopping = True
        deadline = time.monotonic() + 2.0
        while time.monotonic() < deadline:
            with self._lock:
                if not self._inflight:
                    break
            time.sleep(0.05)

    # -- config / tools -------------------------------------------------------

    def get_config_schema(self) -> List[Dict[str, Any]]:
        return [
            {"key": "project", "description": "chatlog project for this profile's notes (default: <profile>-fleet)", "default": ""},
        ]

    def save_config(self, values: Dict[str, Any], hermes_home: str) -> None:
        path = Path(hermes_home) / "chatlog.json"
        cfg: Dict[str, Any] = {}
        try:
            cfg = json.loads(path.read_text())
        except Exception:
            pass
        cfg.update({k: v for k, v in values.items() if v not in (None, "")})
        path.write_text(json.dumps(cfg, indent=2) + "\n")

    def get_tool_schemas(self) -> List[Dict[str, Any]]:
        # The chatlog MCP server already exposes chat_search/chat_get/kv_*;
        # duplicating them here would shadow the mounted tools. Recall is the job.
        return []


def register(ctx) -> None:
    ctx.register_memory_provider(ChatlogMemoryProvider())