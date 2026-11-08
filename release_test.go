// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package chatlog

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The Homebrew formula is generated from a release's SHA256SUMS; one missing entry must stop the release, not publish
// sha256 "".
func TestGenFormula(t *testing.T) {
	dir := t.TempDir()
	var all []string
	for _, p := range []string{"darwin_arm64", "darwin_amd64", "linux_amd64", "linux_arm64"} {
		all = append(all, strings.Repeat("a", 64)+"  chatlog_1.2.3_"+p+".tar.gz")
	}
	gen := func(lines []string) (string, error) {
		sums := filepath.Join(dir, "SHA256SUMS")
		os.WriteFile(sums, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
		out, err := exec.Command("sh", "packaging/homebrew/gen-formula.sh", "v1.2.3", sums).Output()
		return string(out), err
	}
	out, err := gen(all)
	if err != nil || strings.Count(out, `sha256 "`+strings.Repeat("a", 64)+`"`) != 4 ||
		!strings.Contains(out, "https://github.com/atvirokodosprendimai/chatlog/releases/download/v1.2.3/chatlog_1.2.3_linux_amd64.tar.gz") {
		t.Fatalf("full sums: %v\n%s", err, out)
	}
	out, err = gen(all[:3])
	if err == nil || strings.Contains(out, `sha256 ""`) {
		t.Fatalf("a missing entry must fail and write no empty checksum: %v\n%s", err, out)
	}
}
