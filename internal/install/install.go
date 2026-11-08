// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package install merges chatlog's hooks into Claude Code's settings.json and its MCP server into .claude.json, and
// takes them out again, leaving everything else as it was: keys in their order, other hooks and servers untouched.
// Each file is backed up before it changes; a file that does not parse stops the whole run before anything is written.
package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/hook"
	"github.com/atvirokodosprendimai/chatlog/internal/playbook"
)

// HookTimeout is the seconds Claude Code gives one hook before it kills it; the hook's own budget is shorter.
const HookTimeout = 10

// Options says which files and which binary.
type Options struct {
	Bin        string    // absolute path of the chatlog binary the settings run
	Settings   string    // Claude Code's settings.json
	ClaudeJSON string    // Claude Code's .claude.json, which holds user-scope MCP servers
	Skill      string    // the skill file, <claude dir>/skills/chatlog/SKILL.md; "" leaves skills alone
	Uninstall  bool      // take chatlog's entries out instead
	DryRun     bool      // say what would change, write nothing
	Now        time.Time // the backup's suffix
	Out        io.Writer // one line per file: what changed
}

// ours matches a hook command chatlog wrote, whatever the binary's directory: "<path>/chatlog hook <Event>".
var ours = regexp.MustCompile(`(^|[/'"\s])chatlog['"]? hook [A-Za-z]+\s*$`)

// IsOurs says whether a hook command is one of chatlog's.
func IsOurs(command string) bool { return ours.MatchString(command) }

// Command is the hook command for an event.
func Command(bin, event string) string { return shellQuote(bin) + " hook " + event }

func shellQuote(s string) string {
	if regexp.MustCompile(`^[A-Za-z0-9_./+-]+$`).MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type file struct {
	path   string
	before []byte
	exists bool
	obj    object
}

func load(path string) (*file, error) {
	// a symlinked settings file (dotfiles managers do this) is changed where it lives, the link kept
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	f := &file{path: path}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		f.before, f.exists = b, true
	}
	if f.obj, err = parseObject(f.before); err != nil {
		return nil, fmt.Errorf("%s does not parse as a JSON object, nothing was changed: %w", path, err)
	}
	return f, nil
}

// changed says whether the file on disk is no longer what was read: a running Claude Code rewrites .claude.json often.
func (f *file) changed() bool {
	b, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return f.exists
	}
	return err != nil || !f.exists || string(b) != string(f.before)
}

var errRace = errors.New("changed while being merged")

// Run installs or uninstalls. Both files are read and changed in memory first, so one that does not parse writes
// nothing. A file that changes on disk between the read and the write is read and merged again, up to three times.
func Run(o Options) error {
	if !filepath.IsAbs(o.Bin) {
		return fmt.Errorf("the binary path must be absolute: %q", o.Bin)
	}
	var err error
	for range 3 {
		if err = runOnce(o); !errors.Is(err, errRace) {
			break
		}
	}
	if err != nil {
		return err
	}
	return skill(o)
}

func runOnce(o Options) error {
	settings, err := load(o.Settings)
	if err != nil {
		return err
	}
	claude, err := load(o.ClaudeJSON)
	if err != nil {
		return err
	}
	hookNote, err := mergeHooks(&settings.obj, o.Bin, !o.Uninstall)
	if err != nil {
		return fmt.Errorf("%s: %w", o.Settings, err)
	}
	mcpNote, err := mergeMCP(&claude.obj, o.Bin, !o.Uninstall)
	if err != nil {
		return fmt.Errorf("%s: %w", o.ClaudeJSON, err)
	}
	if err := settings.write(o, hookNote); err != nil {
		return err
	}
	if err := claude.write(o, mcpNote); err != nil {
		// hooks without their MCP server would be half an install: put settings.json back as it was read
		if !o.DryRun {
			if rerr := settings.restore(); rerr != nil {
				return errors.Join(err, rerr)
			}
			fmt.Fprintf(o.Out, "%s: put back as it was, since %s could not be written\n", settings.path, claude.path)
		}
		return err
	}
	return nil
}

// restore writes the file back as it was read, or removes it when it did not exist.
func (f *file) restore() error {
	if !f.exists {
		return os.Remove(f.path)
	}
	tmp := fmt.Sprintf("%s.chatlog-tmp-%d", f.path, os.Getpid())
	if err := os.WriteFile(tmp, f.before, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

func (f *file) write(o Options, note string) error {
	after, err := pretty(f.obj)
	if err != nil {
		return err
	}
	if f.exists && string(after) == string(f.before) || !f.exists && len(f.obj) == 0 {
		fmt.Fprintf(o.Out, "%s: unchanged\n", f.path)
		return nil
	}
	if o.DryRun {
		fmt.Fprintf(o.Out, "%s: would change: %s\n", f.path, note)
		return nil
	}
	mode := fs.FileMode(0o600)
	backup := ""
	if f.exists {
		if fi, err := os.Stat(f.path); err == nil {
			mode = fi.Mode().Perm()
		}
		// nanoseconds: two runs in one second must not overwrite the copy of the file as it was before the first
		backup = f.path + ".chatlog-bak-" + o.Now.Format("20060102T150405.000000000")
		if err := os.WriteFile(backup, f.before, mode); err != nil {
			return fmt.Errorf("backup %s: %w", backup, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.chatlog-tmp-%d", f.path, os.Getpid())
	if err := os.WriteFile(tmp, after, mode); err != nil {
		return err
	}
	if f.changed() {
		os.Remove(tmp)
		if backup != "" {
			os.Remove(backup)
		}
		return fmt.Errorf("%s %w", f.path, errRace)
	}
	if err := os.Rename(tmp, f.path); err != nil { // whole or not at all: Claude Code may read it at any moment
		return err
	}
	if backup != "" {
		note += " (backup " + backup + ")"
	}
	fmt.Fprintf(o.Out, "%s: changed: %s\n", f.path, note)
	return nil
}

// skill writes or removes the skill file; a SKILL.md without chatlog's marker is someone else's and stays.
func skill(o Options) error {
	if o.Skill == "" {
		return nil
	}
	b, err := os.ReadFile(o.Skill)
	exists := err == nil
	ours := exists && strings.Contains(string(b), playbook.SkillMarker)
	want := playbook.Skill()
	switch {
	case exists && !ours:
		fmt.Fprintf(o.Out, "%s: not chatlog's, left as it is\n", o.Skill)
	case o.Uninstall && !exists:
		fmt.Fprintf(o.Out, "%s: absent\n", o.Skill)
	case o.Uninstall && o.DryRun:
		fmt.Fprintf(o.Out, "%s: would remove\n", o.Skill)
	case o.Uninstall:
		if err := os.Remove(o.Skill); err != nil {
			return err
		}
		os.Remove(filepath.Dir(o.Skill)) // only when empty
		fmt.Fprintf(o.Out, "%s: removed\n", o.Skill)
	case string(b) == want:
		fmt.Fprintf(o.Out, "%s: unchanged\n", o.Skill)
	case o.DryRun:
		fmt.Fprintf(o.Out, "%s: would write the chatlog skill\n", o.Skill)
	default:
		if err := os.MkdirAll(filepath.Dir(o.Skill), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(o.Skill, []byte(want), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(o.Out, "%s: wrote the chatlog skill\n", o.Skill)
	}
	return nil
}

type handler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

// mergeHooks takes chatlog's hook entries out of every event, then, when adding, appends one group per event of
// hook.Events. Groups of other tools stay where they are; an event or "hooks" left empty by the removal goes too.
func mergeHooks(settings *object, bin string, add bool) (string, error) {
	hooks, err := settings.subObject("hooks")
	if err != nil {
		return "", err
	}
	_, hadHooks := settings.get("hooks")
	hadMembers := len(hooks) > 0
	removed := 0
	var emptied []string // events whose last entries were ours: those keys go; an event the user left empty stays
	for i := range hooks {
		ev := hooks[i].key
		var groups []json.RawMessage
		if err := json.Unmarshal(hooks[i].value, &groups); err != nil {
			return "", fmt.Errorf("hooks.%s is not a list: %w", ev, err)
		}
		n := 0
		kept := groups[:0]
		for _, g := range groups {
			g2, k, err := withoutOurs(g)
			if err != nil {
				return "", fmt.Errorf("hooks.%s: %w", ev, err)
			}
			n += k
			if g2 != nil {
				kept = append(kept, g2)
			}
		}
		if n == 0 {
			continue
		}
		removed += n
		if len(kept) == 0 {
			emptied = append(emptied, ev)
		}
		hooks[i].value = raw(kept)
	}
	for _, k := range emptied {
		hooks.del(k)
	}
	if add {
		for _, ev := range hook.Events {
			g := object{}
			if slices.Contains(hook.ToolEvents, ev) {
				g.set("matcher", raw("*"))
			}
			g.set("hooks", raw([]handler{{Type: "command", Command: Command(bin, ev), Timeout: HookTimeout}}))
			var groups []json.RawMessage
			if v, ok := hooks.get(ev); ok {
				json.Unmarshal(v, &groups) // parsed above
			}
			gb, _ := g.MarshalJSON()
			hooks.set(ev, raw(append(groups, gb)))
		}
	}
	switch {
	case len(hooks) > 0:
		settings.set("hooks", raw(hooks))
	case hadMembers || !hadHooks:
		settings.del("hooks") // emptied by this removal, or never there
	}
	if add {
		return fmt.Sprintf("%d chatlog hooks set (%d old ones replaced)", len(hook.Events), removed), nil
	}
	return fmt.Sprintf("%d chatlog hooks removed", removed), nil
}

// withoutOurs returns a hook group without chatlog's handlers and how many it took out; nil when nothing is left.
// A group with none of ours comes back byte for byte.
func withoutOurs(g json.RawMessage) (json.RawMessage, int, error) {
	obj, err := parseObject(g)
	if err != nil {
		return nil, 0, err
	}
	v, ok := obj.get("hooks")
	if !ok {
		return g, 0, nil
	}
	var hs []json.RawMessage
	if err := json.Unmarshal(v, &hs); err != nil {
		return nil, 0, fmt.Errorf("a group's hooks is not a list: %w", err)
	}
	kept := hs[:0]
	for _, h := range hs {
		var x handler
		if json.Unmarshal(h, &x) == nil && IsOurs(x.Command) {
			continue
		}
		kept = append(kept, h)
	}
	n := len(hs) - len(kept)
	switch {
	case n == 0:
		return g, 0, nil
	case len(kept) == 0:
		return nil, n, nil
	}
	obj.set("hooks", raw(kept))
	b, _ := obj.MarshalJSON()
	return b, n, nil
}

// MCPName is the server's name in mcpServers.
const MCPName = "chatlog"

type mcpEntry struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// mergeMCP sets or removes mcpServers.chatlog. Removal takes only an entry whose command is a chatlog binary.
func mergeMCP(claude *object, bin string, add bool) (string, error) {
	servers, err := claude.subObject("mcpServers")
	if err != nil {
		return "", err
	}
	_, had := servers.get(MCPName)
	note := ""
	if add {
		servers.set(MCPName, raw(mcpEntry{Type: "stdio", Command: bin, Args: []string{"mcp"}, Env: map[string]string{}}))
		note = "mcpServers.chatlog set to " + bin + " mcp"
	} else if had {
		v, _ := servers.get(MCPName)
		var e mcpEntry
		if json.Unmarshal(v, &e) != nil || filepath.Base(e.Command) != "chatlog" {
			return "mcpServers.chatlog is not a chatlog binary: left as it is", nil
		}
		servers.del(MCPName)
		note = "mcpServers.chatlog removed"
	} else {
		return "no chatlog server to remove", nil
	}
	if len(servers) == 0 && (had || !add) {
		claude.del("mcpServers")
	} else {
		claude.set("mcpServers", raw(servers))
	}
	return note, nil
}

// Missing lists what an install should have put in place and is not there, for `chatlog doctor`.
func Missing(o Options) ([]string, error) {
	var out []string
	settings, err := load(o.Settings)
	if err != nil {
		return nil, err
	}
	hooks, err := settings.obj.subObject("hooks")
	if err != nil {
		return nil, err
	}
	for _, ev := range hook.Events {
		bin := hookBinary(hooks, ev)
		if bin == "" {
			out = append(out, "hook "+ev+" in "+o.Settings)
		} else if _, err := os.Stat(bin); err != nil {
			out = append(out, "the binary "+bin+" that hook "+ev+" runs")
		}
	}
	claude, err := load(o.ClaudeJSON)
	if err != nil {
		return nil, err
	}
	servers, err := claude.obj.subObject("mcpServers")
	if err != nil {
		return nil, err
	}
	v, ok := servers.get(MCPName)
	var e mcpEntry
	if !ok || json.Unmarshal(v, &e) != nil {
		out = append(out, "mcpServers.chatlog in "+o.ClaudeJSON)
	} else if _, err := os.Stat(e.Command); err != nil {
		out = append(out, "the MCP server's binary "+e.Command)
	}
	if o.Skill != "" {
		if b, err := os.ReadFile(o.Skill); err != nil || !strings.Contains(string(b), playbook.FirstLine) {
			out = append(out, "the skill "+o.Skill)
		}
	}
	return out, nil
}

// Recorded lists the chatlog binaries the settings run, hooks and MCP server, each once.
func Recorded(o Options) ([]string, error) {
	settings, err := load(o.Settings)
	if err != nil {
		return nil, err
	}
	hooks, err := settings.obj.subObject("hooks")
	if err != nil {
		return nil, err
	}
	var out []string
	add := func(b string) {
		if b != "" && !slices.Contains(out, b) {
			out = append(out, b)
		}
	}
	for _, ev := range hook.Events {
		add(hookBinary(hooks, ev))
	}
	claude, err := load(o.ClaudeJSON)
	if err != nil {
		return nil, err
	}
	servers, err := claude.obj.subObject("mcpServers")
	if err != nil {
		return nil, err
	}
	var e mcpEntry
	if v, ok := servers.get(MCPName); ok && json.Unmarshal(v, &e) == nil {
		add(e.Command)
	}
	return out, nil
}

// hookBinary is the binary chatlog's hook for an event runs, "" when the event has none of chatlog's hooks.
func hookBinary(hooks object, ev string) string {
	v, _ := hooks.get(ev)
	var groups []struct {
		Hooks []handler `json:"hooks"`
	}
	json.Unmarshal(v, &groups)
	for _, g := range groups {
		for _, h := range g.Hooks {
			if IsOurs(h.Command) && strings.HasSuffix(h.Command, " hook "+ev) {
				bin := strings.TrimSuffix(h.Command, " hook "+ev)
				if strings.HasPrefix(bin, "'") {
					bin = strings.ReplaceAll(strings.Trim(bin, "'"), `'\''`, "'")
				}
				return bin
			}
		}
	}
	return ""
}
