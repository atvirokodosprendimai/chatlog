// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/install"
	"github.com/atvirokodosprendimai/chatlog/internal/playbook"
	"github.com/atvirokodosprendimai/chatlog/internal/testenv"
)

// These tests run the built binary in a throwaway HOME, as a person would on a new machine; nothing outside t.TempDir
// is read or written.

var built string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "chatlog-bin-")
	if err != nil {
		panic(err)
	}
	// resolved: on macOS the temp dir is under /var, a link to /private/var, and doctor compares resolved paths
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		panic(err)
	}
	built = filepath.Join(dir, "chatlog")
	if out, err := exec.Command("go", "build", "-o", built, ".").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := testenv.Main(m) // HOME, CLAUDE_CONFIG_DIR, XDG_* and CHATLOG_* point at a throwaway dir from here on
	os.RemoveAll(dir)
	os.Exit(code)
}

type home struct {
	t   *testing.T
	dir string
	env []string
}

// newHome is a throwaway home and the whole environment a command run there gets: nothing is inherited, so the
// machine's CLAUDE_CONFIG_DIR, XDG_* or CHATLOG_* cannot leak in, and PATH holds only Go and the system's tools, so
// no `chatlog` installed on this machine can be found (tests run the built binary by its absolute path).
func newHome(t *testing.T) *home {
	t.Helper()
	dir := t.TempDir()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + dir, "PATH=" + filepath.Dir(goBin) + ":/usr/bin:/bin", "CHATLOG_AUTOSTART=0", "TZ=UTC",
		"GOCACHE=" + os.Getenv("GOCACHE"), "GOMODCACHE=" + os.Getenv("GOMODCACHE"), "GOFLAGS=-mod=mod"}
	return &home{t: t, dir: dir, env: env}
}

// The environment a test command gets names no path under the real home, and finds no other chatlog on its PATH.
func TestTestsStayOutOfTheRealHome(t *testing.T) {
	h := newHome(t)
	for _, kv := range h.env {
		k, v, _ := strings.Cut(kv, "=")
		if k == "HOME" || strings.HasPrefix(k, "XDG_") || strings.HasPrefix(k, "CLAUDE") || strings.HasPrefix(k, "CHATLOG_") {
			testenv.Check(t, v)
		}
	}
	for _, d := range filepath.SplitList(strings.TrimPrefix(h.env[1], "PATH=")) {
		if _, err := os.Stat(filepath.Join(d, "chatlog")); err == nil {
			t.Fatalf("a chatlog binary is on the tests' PATH in %s", d)
		}
	}
	testenv.Check(t, os.Getenv("HOME"), os.Getenv("CLAUDE_CONFIG_DIR"), os.Getenv("XDG_DATA_HOME"), os.Getenv("XDG_CONFIG_HOME"))
}

func (h *home) path(p string) string { return filepath.Join(h.dir, p) }

func (h *home) run(stdin string, args ...string) (string, error) {
	h.t.Helper()
	c := exec.Command(built, args...)
	c.Env, c.Dir = h.env, h.dir
	c.Stdin = strings.NewReader(stdin)
	out, err := c.CombinedOutput()
	return string(out), err
}

func (h *home) write(p, s string) {
	h.t.Helper()
	os.MkdirAll(filepath.Dir(h.path(p)), 0o700)
	if err := os.WriteFile(h.path(p), []byte(s), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *home) read(p string) string {
	h.t.Helper()
	b, err := os.ReadFile(h.path(p))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

func sameJSON(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal([]byte(a), &x); err != nil {
		t.Fatalf("%v: %s", err, a)
	}
	if err := json.Unmarshal([]byte(b), &y); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return reflect.DeepEqual(x, y)
}

const userSettings = `{
  "model": "opus",
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/opt/guard.sh <x> & y", "timeout": 5}]}
    ],
    "Notification": [
      {"hooks": [{"type": "command", "command": "notify-send hi"}]}
    ]
  },
  "permissions": {"allow": ["Bash(ls:*)"]}
}
`

const userClaude = `{"numStartups": 3, "mcpServers": {"other": {"type": "stdio", "command": "/usr/bin/other", "args": []}}, "projects": {"/x": {}}}`

func TestInstallKeepsWhatIsThereAndUninstallRestoresIt(t *testing.T) {
	h := newHome(t)
	h.write(".claude/settings.json", userSettings)
	h.write(".claude.json", userClaude)

	if out, err := h.run("", "install", "--dry-run"); err != nil || !strings.Contains(out, "would change") {
		t.Fatalf("dry run: %v %s", err, out)
	}
	if h.read(".claude/settings.json") != userSettings || h.read(".claude.json") != userClaude {
		t.Fatal("a dry run wrote")
	}

	out, err := h.run("", "install")
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	s := h.read(".claude/settings.json")
	for _, want := range []string{`"model": "opus"`, `"/opt/guard.sh <x> & y"`, `"notify-send hi"`, `"Bash(ls:*)"`, " hook SessionEnd\"", " hook PreToolUse\"", `"matcher": "*"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("settings lost or lack %q:\n%s", want, s)
		}
	}
	if strings.Index(s, `"model"`) > strings.Index(s, `"hooks"`) || strings.Index(s, `"hooks"`) > strings.Index(s, `"permissions"`) {
		t.Fatalf("key order changed:\n%s", s)
	}
	if strings.Index(s, "/opt/guard.sh") > strings.Index(s, " hook PreToolUse") {
		t.Fatalf("the person's hook must stay first:\n%s", s)
	}
	c := h.read(".claude.json")
	var cj struct {
		NumStartups int `json:"numStartups"`
		MCP         map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	json.Unmarshal([]byte(c), &cj)
	if cj.NumStartups != 3 || cj.MCP["other"].Command != "/usr/bin/other" || cj.MCP["chatlog"].Command != built || cj.MCP["chatlog"].Args[0] != "mcp" {
		t.Fatalf(".claude.json: %s", c)
	}
	backups, _ := filepath.Glob(h.path(".claude/settings.json.chatlog-bak-*"))
	if len(backups) != 1 || h.read(strings.TrimPrefix(backups[0], h.dir+"/")) != userSettings {
		t.Fatalf("backup: %v", backups)
	}

	if out, err := h.run("", "install"); err != nil || strings.Count(out, "unchanged") != 3 || strings.Contains(out, "starter notes") {
		t.Fatalf("a second run must change nothing: %v %s", err, out)
	}
	if h.read(".claude/settings.json") != s {
		t.Fatal("second run rewrote settings")
	}

	out, err = h.run("", "doctor")
	for _, want := range []string{"ok    Claude Code settings", "ok    MCP server answers initialize", "ok    every hook on a synthetic event", "ok    starter notes", "-byte digest after a compaction"} {
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("doctor after install lacks %q: %v\n%s", want, err, out)
		}
	}
	if !strings.Contains(h.read(".claude/skills/chatlog/SKILL.md"), playbook.FirstLine) {
		t.Fatal("the skill was not written")
	}
	if out, err := h.run("", "install", "--uninstall"); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if !sameJSON(t, h.read(".claude/settings.json"), userSettings) || !sameJSON(t, h.read(".claude.json"), userClaude) {
		t.Fatalf("uninstall did not restore:\n%s\n%s", h.read(".claude/settings.json"), h.read(".claude.json"))
	}
	if out, _ := h.run("", "doctor"); !strings.Contains(out, "FAIL  Claude Code settings") {
		t.Fatalf("doctor must see the hooks gone:\n%s", out)
	}
	if _, err := os.Stat(h.path(".claude/skills/chatlog")); !os.IsNotExist(err) {
		t.Fatalf("uninstall left the skill: %v", err)
	}
}

func TestIsOurs(t *testing.T) {
	for cmd, want := range map[string]bool{
		"/x/chatlog hook Stop":               true,
		"chatlog hook PreToolUse":            true,
		"'/a dir/chatlog' hook SessionStart": true,
		"/x/mychatlog hook Stop":             false,
		"/x/chatlog-v2 hook Stop":            false,
		"/x/chatlog hook Stop && rm -rf x":   false,
		"bash ~/hooks/chatlog.sh":            false,
	} {
		if install.IsOurs(cmd) != want {
			t.Errorf("IsOurs(%q) = %v", cmd, !want)
		}
	}
}

func TestUninstallTakesOnlyOurs(t *testing.T) {
	h := newHome(t)
	// a group holding someone's hook and ours, an event the user keeps empty, an empty hooks object elsewhere
	mixed := `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "notify"}, {"type": "command", "command": "/old/chatlog hook Stop"}]}], "Notification": []}}`
	h.write(".claude/settings.json", mixed)
	h.write(".claude.json", `{"mcpServers": {"chatlog": {"type": "stdio", "command": "/usr/bin/someone-elses-tool", "args": []}}}`)
	if out, err := h.run("", "install", "--uninstall"); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if !sameJSON(t, h.read(".claude/settings.json"), `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "notify"}]}], "Notification": []}}`) {
		t.Fatalf("only chatlog's handler goes, the user's empty event stays:\n%s", h.read(".claude/settings.json"))
	}
	if !strings.Contains(h.read(".claude.json"), "someone-elses-tool") {
		t.Fatal("an mcpServers.chatlog that is not a chatlog binary must stay")
	}
	// a fresh machine: install then uninstall leaves no chatlog entry
	f := newHome(t)
	f.run("", "install")
	if out, err := f.run("", "install", "--uninstall"); err != nil || strings.Contains(f.read(".claude/settings.json"), "chatlog") || strings.Contains(f.read(".claude.json"), "chatlog") {
		t.Fatalf("%v %s\n%s\n%s", err, out, f.read(".claude/settings.json"), f.read(".claude.json"))
	}
}

func TestDoctorFailsWhenTheHookBinaryIsGone(t *testing.T) {
	h := newHome(t)
	if out, err := h.run("", "install", "--bin", "/nonexistent/chatlog"); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if out, err := h.run("", "doctor"); err == nil || !strings.Contains(out, "the binary /nonexistent/chatlog that hook") {
		t.Fatalf("doctor must fail on a missing hook binary: %v\n%s", err, out)
	}
}

func TestBrokenConfigDoesNotBlockTheRepairPath(t *testing.T) {
	h := newHome(t)
	h.write(".config/chatlog/config.json", `{"hookz": 1}`)
	if out, err := h.run("", "install"); err != nil || !strings.Contains(out, "warning, defaults used") {
		t.Fatalf("install with a broken config: %v %s", err, out)
	}
	if out, err := h.run("", "install", "--uninstall"); err != nil {
		t.Fatalf("uninstall with a broken config: %v %s", err, out)
	}
	if out, err := h.run("", "events"); err == nil || !strings.Contains(out, "the keys it may hold") {
		t.Fatalf("other commands stop and say how to fix it: %v %s", err, out)
	}
}

func TestDeepDataDirStillServes(t *testing.T) {
	h := newHome(t)
	deep := h.path(strings.Repeat("d", 120))
	if len(deep) < 120 {
		t.Fatal("the data dir must be deeper than a socket path may be")
	}
	srv := exec.Command(built, "--data-dir", deep, "server", "--source", h.path("none"))
	srv.Env = h.env
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { srv.Process.Kill(); srv.Wait() }()
	var out string
	var err error
	for range 50 {
		if out, err = h.run("", "--data-dir", deep, "status"); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil || !strings.Contains(out, `"chunks"`) {
		t.Fatalf("status over the default socket of a 120-character data dir: %v %s", err, out)
	}
	if out, err := h.run("", "--data-dir", deep, "server", "--stop"); err != nil || !strings.Contains(out, "stopped") {
		t.Fatalf("server --stop: %v %s", err, out)
	}
}

func TestIngestSaysWhatWasNew(t *testing.T) {
	h := newHome(t)
	h.write("t.jsonl", `{"type":"user","uuid":"u1","sessionId":"s","cwd":"/u/x","timestamp":"2026-11-08T09:00:00Z","origin":{"kind":"human"},"message":{"content":"hi"}}`+"\n")
	if out, _ := h.run("", "ingest", h.path("t.jsonl")); !strings.Contains(out, ": 1 new exchanges") {
		t.Fatalf("first read: %s", out)
	}
	if out, _ := h.run("", "ingest", h.path("t.jsonl")); !strings.Contains(out, ": 0 new exchanges") {
		t.Fatalf("second read: %s", out)
	}
}

func TestInstallOnAFreshMachineAndClaudeConfigDir(t *testing.T) {
	h := newHome(t)
	h.env = append(h.env, "CLAUDE_CONFIG_DIR="+h.path("cc"))
	if out, err := h.run("", "install"); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if !strings.Contains(h.read("cc/settings.json"), " hook UserPromptSubmit") || !strings.Contains(h.read("cc/.claude.json"), `"chatlog"`) {
		t.Fatal("CLAUDE_CONFIG_DIR not honoured")
	}
	if _, err := os.Stat(h.path(".local/share/chatlog")); err != nil {
		t.Fatalf("data dir not made: %v", err)
	}
	if fi, _ := os.Stat(h.path("cc/settings.json")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("new settings mode %v", fi.Mode().Perm())
	}
}

func TestInstallRefusesUnparsableSettings(t *testing.T) {
	h := newHome(t)
	bad := `{"hooks": {` // cut short
	h.write(".claude/settings.json", bad)
	h.write(".claude.json", userClaude)
	out, err := h.run("", "install")
	if err == nil || !strings.Contains(out, "does not parse") {
		t.Fatalf("%v %s", err, out)
	}
	if h.read(".claude/settings.json") != bad || h.read(".claude.json") != userClaude {
		t.Fatal("something was written")
	}
	if b, _ := filepath.Glob(h.path(".claude*chatlog-bak-*")); len(b) != 0 {
		t.Fatalf("backup made for a refused run: %v", b)
	}
}

func TestHookNeverFailsTheSession(t *testing.T) {
	h := newHome(t)
	h.write("a-file", "not a directory")
	for name, c := range map[string]struct{ stdin, dataDir string }{
		"garbage input":        {"not json", h.path("d1")},
		"unknown event":        {`{"hook_event_name":"Nope"}`, h.path("d2")},
		"data dir unwritable":  {`{"session_id":"s"}`, h.path("a-file/d")},
		"transcript not there": {`{"session_id":"s","transcript_path":"/nowhere/x.jsonl"}`, h.path("d3")},
	} {
		c2 := exec.Command(built, "--data-dir", c.dataDir, "hook", "Stop")
		c2.Env, c2.Stdin = h.env, strings.NewReader(c.stdin)
		out, err := c2.Output()
		if err != nil || len(out) != 0 {
			t.Fatalf("%s: exit %v, stdout %q", name, err, out)
		}
	}
	if log := h.read("d1/hooks.log"); !strings.Contains(log, "hook input") {
		t.Fatalf("failure not logged: %q", log)
	}
	h.write("bad-config.json", `{"nope": 1}`)
	c := exec.Command(built, "--data-dir", h.path("d4"), "hook", "UserPromptSubmit")
	c.Env, c.Stdin = append(h.env, "CHATLOG_CONFIG="+h.path("bad-config.json")), strings.NewReader(`{"session_id":"s","prompt":"x"}`)
	if out, err := c.Output(); err != nil || !strings.Contains(string(out), "additionalContext") {
		t.Fatalf("a bad config must not stop the hook: %v %q", err, out)
	}
}

func TestInstallScript(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary through install.sh")
	}
	h := newHome(t)
	h.write(".claude/settings.json", userSettings)
	h.env = append(h.env, "CHATLOG_BIN_DIR="+h.path("bin"))
	script, _ := filepath.Abs("../../install.sh")
	sh := func(args ...string) (string, error) {
		c := exec.Command("sh", append([]string{script}, args...)...)
		c.Env = h.env
		out, err := c.CombinedOutput()
		return string(out), err
	}
	if out, err := sh("--dry-run"); err != nil || !strings.Contains(out, "would change") {
		t.Fatalf("%v %s", err, out)
	}
	if h.read(".claude/settings.json") != userSettings {
		t.Fatal("dry run wrote")
	}
	if _, err := os.Stat(h.path("bin/chatlog")); err == nil {
		t.Fatal("dry run installed the binary")
	}
	out, err := sh()
	if err != nil || !strings.Contains(out, "ok    every hook on a synthetic event") || !strings.Contains(out, "files read") {
		t.Fatalf("install.sh: %v\n%s", err, out)
	}
	if !strings.Contains(h.read(".claude/settings.json"), h.path("bin/chatlog")+" hook Stop") {
		t.Fatal("settings do not run the installed binary")
	}
	if out, err := sh("--uninstall"); err != nil || !sameJSON(t, h.read(".claude/settings.json"), userSettings) {
		t.Fatalf("uninstall: %v %s", err, out)
	}
}

// After a compaction the session gets back the previous turns with their tool calls, within the cap.
func TestWakeAfterCompactionCarriesToolCalls(t *testing.T) {
	h := newHome(t)
	var lines []string
	for i, tool := range []string{"Bash", "Grep", "Edit"} {
		lines = append(lines,
			fmt.Sprintf(`{"type":"user","uuid":"u%d","sessionId":"s","cwd":"/u/webshop","timestamp":"2026-11-08T09:0%d:00Z","origin":{"kind":"human"},"message":{"content":"step %d"}}`, i, i, i),
			fmt.Sprintf(`{"type":"assistant","uuid":"a%d","sessionId":"s","timestamp":"2026-11-08T09:0%d:01Z","message":{"content":[{"type":"tool_use","id":"t%d","name":"%s","input":{"command":"x"}}]}}`, i, i, i, tool),
			fmt.Sprintf(`{"type":"user","uuid":"r%d","sessionId":"s","timestamp":"2026-11-08T09:0%d:02Z","message":{"content":[{"type":"tool_result","tool_use_id":"t%d","content":"ok"}]}}`, i, i, i))
	}
	h.write("s.jsonl", strings.Join(lines, "\n")+"\n")
	h.run("", "ingest", h.path("s.jsonl"))
	out, err := h.run("", "wake", "--kind", "compact", "--project", "webshop", "--bytes", "6000")
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	for _, want := range []string{playbook.FirstLine, "tool Bash 09:00:01Z 1s", "tool Grep", "tool Edit", "step 2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("wake lacks %q:\n%s", want, out)
		}
	}
	if len(out) > 6000 {
		t.Fatalf("over the cap: %d", len(out))
	}
}

// A session open longer than the server's idle time: the server is gone, the next tool call starts it again.
func TestMCPRevivesAStoppedServer(t *testing.T) {
	h := newHome(t)
	data := h.path("d")
	c := exec.Command(built, "--data-dir", data, "mcp")
	c.Env = append(slices.DeleteFunc(slices.Clone(h.env), func(s string) bool { return strings.HasPrefix(s, "CHATLOG_AUTOSTART=") }), "CHATLOG_AUTOSTART=1")
	in, _ := c.StdinPipe()
	outp, _ := c.StdoutPipe()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		in.Close()
		c.Wait()
		h.run("", "--data-dir", data, "server", "--stop")
	}()
	rd := bufio.NewReader(outp)
	call := func(id int, msg string) string {
		fmt.Fprintln(in, msg)
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("mcp ended: %v", err)
			}
			if strings.Contains(line, fmt.Sprintf(`"id":%d`, id)) {
				return line
			}
		}
	}
	call(1, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	fmt.Fprintln(in, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	status := func(id int) string {
		return call(id, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"chat_status","arguments":{}}}`, id))
	}
	var first string
	for i := 2; i < 40; i++ { // the autostarted server takes a moment
		if first = status(i); strings.Contains(first, "chunks") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(first, "chunks") {
		t.Fatalf("no answer from the autostarted server: %s", first)
	}
	if out, err := h.run("", "--data-dir", data, "server", "--stop"); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if again := status(100); !strings.Contains(again, "chunks") {
		t.Fatalf("the tool did not come back after the server stopped: %s", again)
	}
}

// Installed through a package manager's link, the settings must run the link, not the versioned file behind it.
func TestInstallRecordsTheStableLink(t *testing.T) {
	h := newHome(t)
	cellar := h.path("Cellar/chatlog/1.0/bin/chatlog")
	os.MkdirAll(filepath.Dir(cellar), 0o755)
	b, _ := os.ReadFile(built)
	os.WriteFile(cellar, b, 0o755)
	link := h.path("prefix/bin/chatlog")
	os.MkdirAll(filepath.Dir(link), 0o755)
	if err := os.Symlink(cellar, link); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(link, "install")
	c.Env = append(slices.DeleteFunc(slices.Clone(h.env), func(s string) bool { return strings.HasPrefix(s, "PATH=") }),
		"PATH="+filepath.Dir(link)+":"+os.Getenv("PATH"))
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	s := h.read(".claude/settings.json")
	if !strings.Contains(s, link+" hook Stop") || strings.Contains(s, "Cellar") {
		t.Fatalf("the settings must run the link %s:\n%s", link, s)
	}
	h.run("", "install", "--bin", cellar)
	if out, _ := h.run("", "doctor"); !strings.Contains(out, "note  binary path: the settings run "+cellar) {
		t.Fatalf("doctor must warn about a versioned Cellar path:\n%s", out)
	}
}

// When .claude.json cannot be written, settings.json goes back as it was: no hooks without their MCP server.
func TestInstallRollsBackHalfAnInstall(t *testing.T) {
	h := newHome(t)
	h.write(".claude/settings.json", userSettings)
	h.write("ro/claude.json", userClaude)
	os.Chmod(h.path("ro"), 0o500)
	defer os.Chmod(h.path("ro"), 0o700)
	out, err := h.run("", "install", "--claude-json", h.path("ro/claude.json"))
	if err == nil || !strings.Contains(out, "put back as it was") {
		t.Fatalf("%v %s", err, out)
	}
	if h.read(".claude/settings.json") != userSettings {
		t.Fatalf("settings not restored:\n%s", h.read(".claude/settings.json"))
	}
}
