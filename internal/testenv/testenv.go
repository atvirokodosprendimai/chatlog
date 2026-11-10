// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package testenv points every variable chatlog takes a path from at a throwaway directory before a package's tests
// run, so no test can read or write the real ~/.claude, ~/.claude.json, config or data dir, nor ingest the machine's
// own transcripts. Every package with tests calls Main from its TestMain.
package testenv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// RealHome is the home directory before Main replaced it; Check refuses paths under it.
var RealHome string

// Main isolates the environment, runs the tests and removes the throwaway directory.
func Main(m *testing.M) int {
	RealHome, _ = os.UserHomeDir()
	// the build caches stay where they are: a new HOME would move them, and a test that builds would start cold
	for _, k := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
		if os.Getenv(k) == "" {
			if out, err := exec.Command("go", "env", k).Output(); err == nil {
				os.Setenv(k, strings.TrimSpace(string(out)))
			}
		}
	}
	dir, err := os.MkdirTemp("", "chatlog-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	home := filepath.Join(dir, "home")
	run := filepath.Join(dir, "run")
	for _, d := range []string{home, run} {
		os.MkdirAll(d, 0o700)
	}
	for k, v := range map[string]string{
		"HOME": home, "CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude"), "XDG_DATA_HOME": filepath.Join(home, ".local/share"),
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_RUNTIME_DIR": run,
	} {
		os.Setenv(k, v)
	}
	for _, k := range []string{"CHATLOG_DIR", "CHATLOG_CONFIG", "CHATLOG_SOCKET", "CHATLOG_AUTOSTART", "CHATLOG_PROJECT"} {
		os.Unsetenv(k)
	}
	os.Setenv("CHATLOG_AUTOSTART", "0") // no test starts a background server unless it asks for one
	return m.Run()
}

// Check fails t when any path lies under the real home: the guard that the isolation above held.
func Check(t testing.TB, paths ...string) {
	t.Helper()
	if RealHome == "" {
		t.Fatal("testenv.Main did not run: add func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }")
	}
	for _, p := range paths {
		if p == RealHome || strings.HasPrefix(p, RealHome+string(filepath.Separator)) {
			t.Fatalf("%s is under the real home %s: a test must never touch it", p, RealHome)
		}
	}
}
