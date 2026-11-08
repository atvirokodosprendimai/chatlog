// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atvirokodosprendimai/chatlog/internal/testenv"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	t.Setenv("CHATLOG_CONFIG", p)
	if f, err := Load(); err != nil || f.Hooks.Max() != DefaultMaxField || !f.Hooks.On("time") {
		t.Fatalf("a missing file is the defaults: %+v %v", f, err)
	}
	cases := map[string]string{
		`{"hooks":{"off":["tools"],"max_field_bytes":50},"tei":"http://e:1"}`: "",
		`{"hook":{}}`:                 "the keys it may hold: sources",
		`{"hooks":{"off":["clock"]}}`: "not a piece; the pieces: time",
		`{"timezone":"Mars/Olympus"}`: "not a zone this system knows",
		`{"sources":["/a"],`:          "fix or move the file",
	}
	for body, want := range cases {
		os.WriteFile(p, []byte(body), 0o600)
		f, err := Load()
		if want == "" {
			if err != nil || f.Hooks.On("tools") || !f.Hooks.On("time") || f.Hooks.Max() != 50 || f.TEI != "http://e:1" {
				t.Fatalf("%s: %+v %v", body, f, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: error %v, want %q", body, err, want)
		}
	}
}

func TestPaths(t *testing.T) {
	t.Setenv("HOME", "/h")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "/xc")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if DataDir() != "/h/.local/share/chatlog" || Dir() != "/xc/chatlog" || ClaudeJSON() != "/h/.claude.json" || len(Sources()) != 1 {
		t.Fatalf("%s %s %s %v", DataDir(), Dir(), ClaudeJSON(), Sources())
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "/cc")
	if ClaudeDir() != "/cc" || ClaudeJSON() != "/cc/.claude.json" || strings.Join(Sources(), ",") != "/cc/projects,/h/.claude/projects" {
		t.Fatalf("%s %s %v", ClaudeDir(), ClaudeJSON(), Sources())
	}
}

// Every path chatlog would use during the tests lies outside the real home (testenv.Main moved them all).
func TestPathsStayOutOfTheRealHome(t *testing.T) {
	sock, err := Socket(DataDir())
	if err != nil {
		t.Fatal(err)
	}
	testenv.Check(t, append([]string{DataDir(), Dir(), ClaudeDir(), ClaudeJSON(), Path(), sock}, Sources()...)...)
}
