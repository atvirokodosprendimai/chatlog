// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package chatlog

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// forbidden are words the package must not carry in any tracked file or file name: it is general, not one
// organisation's tool. Built from parts so this file does not hold them itself.
var forbidden = []string{"tei" + "sora"}

func TestNoForbiddenWord(t *testing.T) {
	out, err := exec.Command("git", "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files failed: the check must see the tracked files: %v", err)
	}
	files := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	if len(files) < 10 {
		t.Fatalf("git ls-files listed %d files: the check would prove nothing", len(files))
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if os.IsNotExist(err) {
			continue // deleted in the work tree, not yet committed
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range forbidden {
			if strings.Contains(strings.ToLower(f), w) {
				t.Errorf("file name %s holds %q", f, w)
			}
			if i := bytes.Index(bytes.ToLower(b), []byte(w)); i >= 0 {
				t.Errorf("%s:%d holds %q", f, bytes.Count(b[:i], []byte("\n"))+1, w)
			}
		}
	}
}
