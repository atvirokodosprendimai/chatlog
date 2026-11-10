// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package project says which project an exchange belongs to: the base name of the directory the session ran in.
package project

import (
	"os"
	"path/filepath"
	"strings"
)

// Resolve is the project of a call, in one place for everything that reads by project (the tail, the wake digest, the notes):
// the CHATLOG_PROJECT variable when set, since an agent working in a directory that is not its project (a fleet agent in
// ~/work/x for the project y) says so there; else Of(cwd).
func Resolve(cwd string) string {
	if p := strings.TrimSpace(os.Getenv("CHATLOG_PROJECT")); p != "" {
		return p
	}
	return Of(cwd)
}

// Of is the project of a cwd: its last element, "unknown" when the cwd is not known.
func Of(cwd string) string {
	if cwd == "" {
		return "unknown"
	}
	return filepath.Base(cwd)
}
