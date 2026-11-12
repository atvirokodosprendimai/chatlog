// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package wake

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/know"
)

// MaxReminders is how many owed situations a prompt names; the rest are counted.
const MaxReminders = 3

// Reminder is the line an owed situation puts on a prompt: short, naming the command that loads and the key that finishes.
// It says in a line what Report.Outline and Answer say in full (read every page, answer with the LOADED line, write the done key).
func Reminder(a know.Arm, dir string) string {
	return fmt.Sprintf("WAKE NOT DONE (%s, since %s): run chatlog wake --dump %s, read EVERY page in ONE turn in parallel, then kv_write %s = its LOADED line, tell the user.",
		plain(a.Source, 16), a.Since.UTC().Format(time.RFC3339), shellQuote(dir), know.DoneKey(a.Group))
}

// plain is free text cut to max bytes with anything but letters, digits and . _ - turned into ?: it is the hook's input,
// and a newline in it would be a line of its own in the prompt.
func plain(s string, max int) string {
	if len(s) > max {
		s = s[:max]
	}
	return strings.Map(func(r rune) rune {
		if r == '.' || r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return '?'
	}, s)
}

// shellQuote leaves a plain path alone and single-quotes any other.
func shellQuote(s string) string {
	for _, r := range s {
		if !(r == '/' || r == '.' || r == '_' || r == '-' || r == '+' || r == '=' || r == ',' || r == '@' || r == ':' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}

// Reminders is the lines for the owed situations, the first MaxReminders, then a count of the rest; "" when nothing is owed.
func Reminders(arms []know.Arm, dir string) string {
	var lines []string
	for i, a := range arms {
		if i == MaxReminders {
			lines = append(lines, fmt.Sprintf("(+%d more owed: kv_read prefix sit/ and look for /armed)", len(arms)-MaxReminders))
			break
		}
		lines = append(lines, Reminder(a, dir))
	}
	return strings.Join(lines, "\n")
}

// DumpDir is where `chatlog wake --dump` puts a project's pages, and where the reminder tells the session to look: one per
// user and project, not per session (two sessions of one project share it and write the same pages, one at a time). The
// user is in the name so that another user cannot hold the name in a shared temp dir.
func DumpDir(project string) string {
	sum := sha1.Sum([]byte(project))
	return filepath.Join(os.TempDir(), fmt.Sprintf("chatlog-wake-%d-%s", os.Getuid(), hex.EncodeToString(sum[:4])))
}
