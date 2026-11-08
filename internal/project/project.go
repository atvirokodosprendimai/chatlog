// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package project says which project an exchange belongs to: the base name of the directory the session ran in.
package project

import "path/filepath"

// Of is the project of a cwd: its last element, "unknown" when the cwd is not known.
func Of(cwd string) string {
	if cwd == "" {
		return "unknown"
	}
	return filepath.Base(cwd)
}
