// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func (h *home) runIn(dir string, args ...string) (string, error) {
	h.t.Helper()
	c := exec.Command(built, args...)
	c.Env, c.Dir = h.env, dir
	out, err := c.CombinedOutput()
	return string(out), err
}

func (h *home) repo(name string) string {
	h.t.Helper()
	dir := h.path(filepath.Join("work", name))
	os.MkdirAll(filepath.Join(dir, "scripts"), 0o700)
	os.WriteFile(filepath.Join(dir, "f"), []byte(name), 0o600)
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", name}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			h.t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// session writes a one-exchange transcript of a session that ran in cwd and ingests it.
func (h *home) session(cwd, id string) {
	h.t.Helper()
	h.write(id+".jsonl", fmt.Sprintf(`{"type":"user","uuid":"u-%s","sessionId":%q,"cwd":%q,"timestamp":"2026-11-08T09:00:00Z","origin":{"kind":"human"},"message":{"content":"hello"}}`+"\n"+
		`{"type":"assistant","uuid":"a-%s","sessionId":%q,"timestamp":"2026-11-08T09:00:05Z","message":{"content":[{"type":"text","text":"hi"}]}}`+"\n", id, id, cwd, id, id))
	if out, err := h.run("", "ingest", h.path(id+".jsonl")); err != nil {
		h.t.Fatalf("ingest: %v\n%s", err, out)
	}
}

func TestProjectCommandListsNamesAndKeepsOneKeyPerName(t *testing.T) {
	h := newHome(t)
	web, other := h.repo("webshop"), h.repo("billing")
	h.session(filepath.Join(web, "scripts"), "s1")
	h.session(other, "s2")
	out, err := h.run("", "project", "ls")
	if err != nil || !strings.Contains(out, "webshop  (1 exchanges, 0 notes)") || !strings.Contains(out, "billing  (1 exchanges, 0 notes)") {
		t.Fatalf("project ls: %v\n%s", err, out)
	}
	if out, err := h.run("", "project", "alias", "webshop", "gateway-N"); err != nil {
		t.Fatalf("alias: %v\n%s", err, out)
	}
	if out, _ := h.run("", "project", "ls"); !strings.Contains(out, "webshop gateway-N  (1 exchanges") {
		t.Fatalf("both names:\n%s", out)
	}
	if out, err := h.run("", "project", "alias", "billing", "gateway-N"); err == nil || !strings.Contains(out, `alias "gateway-N" names [`) {
		t.Fatalf("a name another project holds must be refused: %v\n%s", err, out)
	}
	if out, err := h.run("", "project", "alias", "-d", "webshop"); err != nil {
		t.Fatalf("alias -d: %v\n%s", err, out)
	}
	// from a subdirectory the project is the repository's, named by its first alias still held
	out, err = h.runIn(filepath.Join(web, "scripts"), "project")
	if err != nil || !strings.HasPrefix(out, "[") || !strings.Contains(out, " gateway-N (key ") || !strings.Contains(out, "found by the repository's root commit") {
		t.Fatalf("project in a subdirectory: %v\n%s", err, out)
	}
	if out, _ := h.runIn(web, "doctor"); !strings.Contains(out, "ok    project: [") {
		t.Fatalf("doctor shows the project it resolved:\n%s", out)
	}
	h.env = append(h.env, "CHATLOG_PROJECT=gateway-N")
	if out, _ := h.runIn(other, "project"); !strings.Contains(out, "found by CHATLOG_PROJECT") || !strings.Contains(out, "gateway-N") {
		t.Fatalf("the override:\n%s", out)
	}
}

// A remote host runs `chatlog mcp` against a forwarded socket and has no data dir, no server and no hooks: the command
// must answer, create nothing, and start nothing.
func TestMCPNeedsNoLocalDataDir(t *testing.T) {
	h := newHome(t)
	data := h.path("no-such-dir/data")
	h.env = append(h.env, "CHATLOG_DIR="+data, "CHATLOG_SOCKET="+h.path("forwarded.sock"), "CHATLOG_PROJECT=webshop-fleet")
	out, err := h.run(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`+"\n", "mcp")
	if err != nil || !strings.Contains(out, "How to use chatlog") {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Dir(data)); err == nil {
		t.Fatalf("chatlog mcp created %s on a host that has no data dir", filepath.Dir(data))
	}
}
