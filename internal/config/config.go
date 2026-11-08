// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package config says where chatlog keeps its data and where Claude Code keeps its own files, and reads config.json.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// File is config.json. Every field is optional; a flag or an environment variable given on the command line wins.
type File struct {
	Sources  []string `json:"sources,omitempty"`  // transcript directories, files or globs; default Sources()
	Since    string   `json:"since,omitempty"`    // YYYY-MM-DD: older exchanges are not indexed
	TEI      string   `json:"tei,omitempty"`      // TEI embedder URL; empty: no vectors, full-text search only
	Reranker string   `json:"reranker,omitempty"` // TEI reranker URL; empty: no rerank
	Timezone string   `json:"timezone,omitempty"` // IANA zone of the local time hooks inject; empty: the system's
	Hooks    Hooks    `json:"hooks"`
}

// Hooks tunes `chatlog hook`.
type Hooks struct {
	Off           []string `json:"off,omitempty"`             // pieces turned off, of Pieces
	MaxFieldBytes int      `json:"max_field_bytes,omitempty"` // cap of a stored prompt, tool input or tool output; default DefaultMaxField
	WakeBytes     int      `json:"wake_bytes,omitempty"`      // cap of the digest injected at session start; default DefaultWakeBytes
}

// The pieces Hooks.Off may name.
const (
	PieceTime   = "time"   // the clock injected at each prompt and tool result
	PieceTools  = "tools"  // a row per tool call
	PieceIngest = "ingest" // the transcript pushed into the history at each turn's end
	PieceRecall = "recall" // the recovery digest injected at session start
)

// Pieces lists them, for validation and messages.
var Pieces = []string{PieceTime, PieceTools, PieceIngest, PieceRecall}

// keys is what config.json may hold, for the message that refuses another key.
const keys = `sources, since, tei, reranker, timezone, hooks {off, max_field_bytes, wake_bytes}`

const (
	// DefaultMaxField is the default cap of a stored field, in bytes.
	DefaultMaxField = 2000
	// DefaultWakeBytes is the default cap of the session-start digest: about 6k tokens at ~4 bytes a token, room for some
	// 40 notes and a dozen turns with their tool calls, small against a 200k-token window; the digest says how to fetch
	// the rest.
	DefaultWakeBytes = 24000
)

// On says whether a piece is on.
func (h Hooks) On(piece string) bool { return !slices.Contains(h.Off, piece) }

// Max is the field cap in force.
func (h Hooks) Max() int {
	if h.MaxFieldBytes > 0 {
		return h.MaxFieldBytes
	}
	return DefaultMaxField
}

// Wake is the digest cap in force.
func (h Hooks) Wake() int {
	if h.WakeBytes > 0 {
		return h.WakeBytes
	}
	return DefaultWakeBytes
}

// Location is the zone of the local time hooks inject.
func (f File) Location() *time.Location {
	if loc, err := time.LoadLocation(f.Timezone); f.Timezone != "" && err == nil {
		return loc
	}
	return time.Local
}

// Path is the config file: $CHATLOG_CONFIG, else config.json in Dir.
func Path() string {
	if p := os.Getenv("CHATLOG_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(Dir(), "config.json")
}

// Load reads the config file. A missing file is the defaults; a file that does not parse, an unknown key or an unknown
// piece is an error, so a typo is not silently ignored.
func Load() (File, error) {
	var f File
	b, err := os.ReadFile(Path())
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return File{}, fmt.Errorf("%s does not read as chatlog's config (%v); the keys it may hold: %s; fix or move the file", Path(), err, keys)
	}
	for _, p := range f.Hooks.Off {
		if !slices.Contains(Pieces, p) {
			return File{}, fmt.Errorf("%s: hooks.off names %q, which is not a piece; the pieces: %s", Path(), p, strings.Join(Pieces, ", "))
		}
	}
	for i, s := range f.Sources { // a shell would expand ~; JSON does not
		if s == "~" || strings.HasPrefix(s, "~/") {
			f.Sources[i] = filepath.Join(home(), s[1:])
		}
	}
	if _, err := time.LoadLocation(f.Timezone); err != nil {
		return File{}, fmt.Errorf("%s: timezone %q is not a zone this system knows (an IANA name such as Europe/Paris)", Path(), f.Timezone)
	}
	return f, nil
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

func xdg(env, fallback string) string {
	if d := os.Getenv(env); d != "" {
		return d
	}
	return filepath.Join(home(), fallback)
}

// DataDir is the default data directory: ${XDG_DATA_HOME:-~/.local/share}/chatlog.
func DataDir() string { return filepath.Join(xdg("XDG_DATA_HOME", ".local/share"), "chatlog") }

// Dir is the configuration directory: ${XDG_CONFIG_HOME:-~/.config}/chatlog.
func Dir() string { return filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "chatlog") }

// ClaudeDir is Claude Code's directory: $CLAUDE_CONFIG_DIR, else ~/.claude.
func ClaudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(home(), ".claude")
}

// ClaudeJSON is the file holding Claude Code's user-scope MCP servers: $CLAUDE_CONFIG_DIR/.claude.json, else ~/.claude.json.
func ClaudeJSON() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, ".claude.json")
	}
	return filepath.Join(home(), ".claude.json")
}

// Socket is the default socket of a data dir, short so it fits a unix socket's path limit (104 bytes on macOS, 108
// on Linux) however deep the data dir: $XDG_RUNTIME_DIR/chatlog-<h>.sock, else <tmp>/chatlog-<uid>/<h>.sock, h a hash
// of the data dir, so two data dirs never share one.
func Socket(dataDir string) (string, error) {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	h := hex.EncodeToString(sum[:4])
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "chatlog-"+h+".sock"), nil
	}
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("chatlog-%d", os.Getuid()))
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	// a directory someone else made first could see or take the socket: only our own, private one will do
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 || !ok || int(st.Uid) != os.Getuid() {
		return "", fmt.Errorf("%s is not a private directory of this user (mode 0700): remove it or set CHATLOG_SOCKET", dir)
	}
	return filepath.Join(dir, h+".sock"), nil
}

// Sources are the default transcript directories: Claude Code's projects, and ~/.claude/projects when that differs.
func Sources() []string {
	out := []string{filepath.Join(ClaudeDir(), "projects")}
	if def := filepath.Join(home(), ".claude", "projects"); def != out[0] {
		out = append(out, def)
	}
	return out
}
