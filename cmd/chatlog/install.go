// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/atvirokodosprendimai/chatlog/internal/config"
	"github.com/atvirokodosprendimai/chatlog/internal/hook"
	"github.com/atvirokodosprendimai/chatlog/internal/ingest"
	"github.com/atvirokodosprendimai/chatlog/internal/install"
	"github.com/atvirokodosprendimai/chatlog/internal/playbook"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

func settingsFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "settings", Value: filepath.Join(config.ClaudeDir(), "settings.json"), Usage: "Claude Code's user settings"},
		&cli.StringFlag{Name: "claude-json", Value: config.ClaudeJSON(), Usage: "Claude Code's file of user-scope MCP servers"},
		&cli.StringFlag{Name: "skill", Value: filepath.Join(config.ClaudeDir(), "skills", "chatlog", "SKILL.md"), Usage: "the chatlog skill file; empty: leave skills alone"},
	}
}

func self() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// stableBin is the path the settings should run: the name PATH finds (or $HOMEBREW_PREFIX/bin/chatlog) when it leads
// to this very binary, else this binary's own path. A package manager's link outlives an upgrade; the versioned file
// it points at (a Homebrew Cellar dir) is deleted by the next cleanup, and every hook with it.
func stableBin() (string, error) {
	me, err := self()
	if err != nil {
		return "", err
	}
	var cands []string
	if p, err := exec.LookPath("chatlog"); err == nil {
		cands = append(cands, p)
	}
	if hp := os.Getenv("HOMEBREW_PREFIX"); hp != "" {
		cands = append(cands, filepath.Join(hp, "bin", "chatlog"))
	}
	for _, c := range cands {
		abs, _ := filepath.Abs(c)
		if r, err := filepath.EvalSymlinks(abs); err == nil && r == me {
			return abs, nil
		}
	}
	return me, nil
}

func installCmd() *cli.Command {
	return &cli.Command{
		Name:  "install",
		Usage: "wire chatlog into Claude Code: its hooks into settings.json, its MCP server into .claude.json; a backup of each file first",
		Flags: append(settingsFlags(),
			&cli.StringFlag{Name: "bin", Usage: "the chatlog binary the settings run; default: this one"},
			&cli.BoolFlag{Name: "dry-run", Usage: "say what would change, write nothing"},
			&cli.BoolFlag{Name: "uninstall", Usage: "take chatlog's entries out; the data dir is kept"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			bin := cmd.String("bin")
			if bin == "" {
				var err error
				if bin, err = stableBin(); err != nil {
					return err
				}
			}
			bin, _ = filepath.Abs(bin)
			if !cmd.Bool("dry-run") && !cmd.Bool("uninstall") {
				for _, d := range []string{dataDir(cmd), config.Dir()} {
					if err := os.MkdirAll(d, 0o700); err != nil {
						return err
					}
				}
			}
			err := install.Run(install.Options{Bin: bin, Settings: cmd.String("settings"), ClaudeJSON: cmd.String("claude-json"), Skill: cmd.String("skill"),
				Uninstall: cmd.Bool("uninstall"), DryRun: cmd.Bool("dry-run"), Now: time.Now(), Out: os.Stdout})
			if err != nil || cmd.Bool("dry-run") || cmd.Bool("uninstall") {
				return err
			}
			// the starter notes, so a new machine begins with the playbook in its notes (chatlog seed does the same alone)
			st, err := openStore(cmd, 10*time.Second)
			if err != nil {
				return err
			}
			defer st.Close()
			missing, err := missingSeeds(ctx, st)
			if err != nil || len(missing) == 0 {
				return err
			}
			set := map[string]string{}
			for _, n := range missing {
				set[n.Key] = n.Value
			}
			if _, err := st.KVWrite(ctx, store.GlobalProject, set, nil); err != nil {
				return err
			}
			fmt.Printf("%d starter notes written to the history (rule/ tool/ legend/)\n", len(set))
			return nil
		},
	}
}

// doctorCheck is one line of the doctor's report.
type doctorCheck struct {
	name string
	run  func(ctx context.Context) (string, error)
	info bool // a failure is reported, not counted
}

func doctorCmd(cfg config.File) *cli.Command {
	return &cli.Command{
		Name:  "doctor",
		Usage: "check the install: settings entries, skill, starter notes, database, MCP initialize, every hook and the session-start digest on synthetic events",
		Flags: settingsFlags(),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			exe, err := self()
			if err != nil {
				return err
			}
			checks := []doctorCheck{
				// a note, not a failure: the settings run the binary by its full path, PATH is for the person
				{name: "binary on PATH", info: true, run: func(context.Context) (string, error) {
					p, err := exec.LookPath("chatlog")
					if err != nil {
						return "", fmt.Errorf("chatlog is not on PATH: add %s to PATH", filepath.Dir(exe))
					}
					if r, _ := filepath.EvalSymlinks(p); r != exe {
						return "", fmt.Errorf("PATH finds %s, this binary is %s", p, exe)
					}
					return p, nil
				}},
				{name: "config", run: func(context.Context) (string, error) {
					_, err := config.Load()
					if _, serr := os.Stat(config.Path()); os.IsNotExist(serr) {
						return config.Path() + " absent: defaults in force", nil
					}
					return config.Path(), err
				}},
				{name: "database", run: func(ctx context.Context) (string, error) {
					st, err := openStore(cmd, 5*time.Second)
					if err != nil {
						return "", err
					}
					defer st.Close()
					s, err := st.Stats(ctx)
					if err != nil {
						return "", err
					}
					evs, err := st.Events(ctx, "", 1)
					last := "none yet"
					if len(evs) > 0 {
						last = evs[0].Time.Local().Format(time.RFC3339) + " " + evs[0].Event
					}
					return fmt.Sprintf("%s: %d exchanges, %d sessions, %d waiting for a vector; last hook record: %s",
						filepath.Join(dataDir(cmd), "chatlog.db"), s.Chunks, s.Sessions, s.Pending, last), err
				}},
				{name: "project", run: func(ctx context.Context) (string, error) {
					st, err := openStore(cmd, 5*time.Second)
					if err != nil {
						return "", err
					}
					defer st.Close()
					cwd, _ := os.Getwd()
					return describeProject(ctx, st, cwd)
				}},
				{name: "Claude Code settings", run: func(context.Context) (string, error) {
					missing, err := install.Missing(install.Options{Settings: cmd.String("settings"), ClaudeJSON: cmd.String("claude-json"), Skill: cmd.String("skill")})
					if err != nil {
						return "", err
					}
					if len(missing) > 0 {
						return "", fmt.Errorf("missing (run chatlog install): %s", strings.Join(missing, "; "))
					}
					return fmt.Sprintf("%d hooks, the MCP server and the skill are in place", len(hook.Events)), nil
				}},
				{name: "binary path", info: true, run: func(context.Context) (string, error) {
					bins, err := install.Recorded(install.Options{Settings: cmd.String("settings"), ClaudeJSON: cmd.String("claude-json")})
					if err != nil {
						return "", err
					}
					for _, b := range bins {
						if strings.Contains(b, "/Cellar/") {
							return "", fmt.Errorf("the settings run %s, inside a versioned Homebrew dir that the next upgrade deletes: run chatlog install --bin $(brew --prefix)/bin/chatlog", b)
						}
					}
					return strings.Join(bins, ", "), nil
				}},
				{name: "starter notes", run: func(ctx context.Context) (string, error) {
					st, err := openStore(cmd, 5*time.Second)
					if err != nil {
						return "", err
					}
					defer st.Close()
					missing, err := missingSeeds(ctx, st)
					if err != nil {
						return "", err
					}
					if len(missing) > 0 {
						return "", fmt.Errorf("%d of %d absent (run chatlog seed)", len(missing), len(playbook.Seed))
					}
					return fmt.Sprintf("all %d there", len(playbook.Seed)), nil
				}},
				{name: "MCP server answers initialize", run: func(ctx context.Context) (string, error) { return mcpInitialize(ctx, exe, dataDir(cmd)) }},
				{name: "every hook on a synthetic event", run: func(ctx context.Context) (string, error) { return hooksFire(ctx, exe, cfg.Hooks.Wake()) }},
				{name: "history", info: true, run: func(ctx context.Context) (string, error) { return backlog(ctx, cmd, cfg) }},
				{name: "search", info: true, run: func(context.Context) (string, error) {
					if cfg.TEI == "" {
						return "", fmt.Errorf("text only: no embedder set (\"tei\" in %s), so no search by meaning", config.Path())
					}
					return "embedder " + cfg.TEI, nil
				}},
				{name: "server", info: true, run: func(context.Context) (string, error) {
					c, err := net.DialTimeout("unix", socketPath(cmd), time.Second)
					if err != nil {
						return "", errors.New("not running: it starts with the first Claude Code session (chatlog mcp), or run chatlog server")
					}
					c.Close()
					return "answers on " + socketPath(cmd), nil
				}},
			}
			failed := 0
			for _, c := range checks {
				detail, err := c.run(ctx)
				switch {
				case err == nil:
					fmt.Printf("ok    %s: %s\n", c.name, detail)
				case c.info:
					fmt.Printf("note  %s: %v\n", c.name, err)
				default:
					failed++
					fmt.Printf("FAIL  %s: %v\n", c.name, err)
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d check(s) failed", failed)
			}
			return nil
		},
	}
}

// mcpInitialize starts `chatlog mcp` as Claude Code would, without starting the server, and reads its initialize answer.
func mcpInitialize(ctx context.Context, exe, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, exe, "--data-dir", dir, "mcp")
	c.Env = append(os.Environ(), "CHATLOG_AUTOSTART=0")
	in, err := c.StdinPipe()
	if err != nil {
		return "", err
	}
	out, err := c.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := c.Start(); err != nil {
		return "", err
	}
	defer func() { in.Close(); c.Wait() }()
	fmt.Fprintln(in, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"chatlog-doctor","version":"0"}}}`)
	line, err := bufio.NewReader(out).ReadBytes('\n')
	if err != nil {
		return "", fmt.Errorf("no answer: %w", err)
	}
	var resp struct {
		Result struct {
			ServerInfo   struct{ Name, Version string } `json:"serverInfo"`
			Instructions string                         `json:"instructions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line, &resp); err != nil || resp.Result.ServerInfo.Name != "chatlog" {
		return "", fmt.Errorf("unexpected answer %q", line)
	}
	if !strings.Contains(resp.Result.Instructions, "How to use chatlog") {
		return "", errors.New("the answer carries no instructions")
	}
	return "chatlog " + resp.Result.ServerInfo.Version + ", with its instructions", nil
}

// backlog says how much of the existing transcripts the history has not read yet.
func backlog(ctx context.Context, cmd *cli.Command, cfg config.File) (string, error) {
	st, err := openStore(cmd, 5*time.Second)
	if err != nil {
		return "", err
	}
	defer st.Close()
	src := cfg.Sources
	if len(src) == 0 {
		src = config.Sources()
	}
	files, err := (&ingest.Ingester{Sources: src}).Files()
	if err != nil {
		return "", err
	}
	behind, n := int64(0), 0
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			continue
		}
		cur, err := st.CursorOf(ctx, f)
		if err != nil {
			return "", err
		}
		if fi.Size() > cur.ReadTo {
			behind += fi.Size() - cur.ReadTo
			n++
		}
	}
	if n > 0 {
		return "", fmt.Errorf("%d of %d transcripts not fully read (%d MB): run chatlog ingest --all, or let the server catch up", n, len(files), behind>>20)
	}
	return fmt.Sprintf("all %d transcripts read", len(files)), nil
}

// hooksFire runs every hook the settings run, on synthetic events, against a throwaway data dir, so the real record
// gets no test rows; then it reads that dir back: one row per record, and the transcript's exchange pushed.
func hooksFire(ctx context.Context, exe string, wakeMax int) (string, error) {
	tmp, err := os.MkdirTemp("", "chatlog-doctor-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	tr := filepath.Join(tmp, "s.jsonl")
	if err := os.WriteFile(tr, []byte(`{"type":"user","uuid":"doctor-u1","sessionId":"doctor","cwd":"/doctor","timestamp":"2026-01-01T00:00:00Z","origin":{"kind":"human"},"message":{"content":"doctor check"}}`+"\n"+
		`{"type":"assistant","uuid":"doctor-a1","sessionId":"doctor","timestamp":"2026-01-01T00:00:01Z","message":{"content":[{"type":"text","text":"ok"}]}}`+"\n"), 0o600); err != nil {
		return "", err
	}
	base := fmt.Sprintf(`"session_id":"doctor","cwd":"/doctor","transcript_path":%q`, tr)
	inputs := map[string]string{
		"SessionStart":     `"source":"compact"`,
		"UserPromptSubmit": `"prompt":"doctor check"`,
		"PreToolUse":       `"tool_name":"Bash","tool_use_id":"doctor-t1","tool_input":{"command":"true"}`,
		"PostToolUse":      `"tool_name":"Bash","tool_use_id":"doctor-t1","tool_input":{"command":"true"},"tool_response":{"stdout":""}`,
		"Stop":             `"stop_hook_active":false`,
		"SubagentStop":     `"stop_hook_active":false`,
		"PreCompact":       `"trigger":"manual"`,
		"SessionEnd":       `"reason":"other"`,
	}
	var slow []string
	digest := 0
	for _, ev := range hook.Events {
		c := exec.CommandContext(ctx, exe, "--data-dir", tmp, "hook", ev)
		c.Stdin = strings.NewReader("{" + base + "," + inputs[ev] + "}")
		c.Env = append(os.Environ(), "CHATLOG_CONFIG="+filepath.Join(tmp, "none.json")) // the mechanism, not the pieces config.json may turn off
		start := time.Now()
		out, err := c.Output()
		took := time.Since(start)
		if err != nil {
			return "", fmt.Errorf("%s exited with %v", ev, err)
		}
		if len(out) > 0 && !json.Valid(out) {
			return "", fmt.Errorf("%s printed what Claude Code cannot read: %q", ev, out)
		}
		if ev == "SessionStart" {
			var o struct {
				H struct {
					Context string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			json.Unmarshal(out, &o)
			if !strings.HasPrefix(o.H.Context, playbook.FirstLine) || !strings.Contains(o.H.Context, "COMPACTED") || len(o.H.Context) > wakeMax {
				return "", fmt.Errorf("the session-start digest after a compaction lacks the rule or passes its %d-byte cap (%d bytes)", wakeMax, len(o.H.Context))
			}
			digest = len(o.H.Context)
		}
		if took > 500*time.Millisecond {
			slow = append(slow, fmt.Sprintf("%s %v", ev, took.Round(time.Millisecond)))
		}
	}
	if b, err := os.ReadFile(hook.LogPath(tmp)); err == nil {
		return "", fmt.Errorf("a hook logged a failure: %s", strings.TrimSpace(string(b)))
	}
	st, err := store.Open(filepath.Join(tmp, "chatlog.db"))
	if err != nil {
		return "", err
	}
	defer st.Close()
	evs, err := st.Events(ctx, "doctor", 100)
	if err != nil {
		return "", err
	}
	if want := len(hook.Events) - 1; len(evs) != want { // PreToolUse and PostToolUse share the call's row
		return "", fmt.Errorf("%d rows recorded, want %d", len(evs), want)
	}
	s, err := st.Stats(ctx)
	if err != nil || s.Chunks != 1 {
		return "", fmt.Errorf("the transcript was not pushed: %d exchanges, %v", s.Chunks, err)
	}
	detail := fmt.Sprintf("%d events recorded, the transcript pushed, a %d-byte digest after a compaction (cap %d)", len(hook.Events), digest, wakeMax)
	if len(slow) > 0 {
		detail += "; slow: " + strings.Join(slow, ", ")
	}
	return detail, nil
}
