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
// The procedure it states is the one `chatlog wake` prints (Report.Outline), so the two live in this package.
func Reminder(a know.Arm, dir string) string {
	src := a.Source // the hook's input, free text: clipped so the line keeps its size
	if len(src) > 16 {
		src = strings.ToValidUTF8(src[:16], "")
	}
	return fmt.Sprintf("WAKE NOT DONE (%s, since %s): run chatlog wake --dump %s, read EVERY page in ONE turn in parallel, then kv_write %s = its LOADED line and tell the user it. Repeats each prompt until done.",
		src, a.Since.UTC().Format(time.RFC3339), dir, know.DoneKey(a.Group))
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
// project, not per session (two sessions of one project share it and write the same pages).
func DumpDir(project string) string {
	sum := sha1.Sum([]byte(project))
	return filepath.Join(os.TempDir(), "chatlog-wake-"+hex.EncodeToString(sum[:4]))
}
