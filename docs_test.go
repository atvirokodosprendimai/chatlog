// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package chatlog

import (
	"os"
	"strings"
	"testing"

	"github.com/atvirokodosprendimai/chatlog/internal/playbook"
)

// The rule has one source; the documents that lead with it must carry it as it is.
func TestDocsLeadWithTheRule(t *testing.T) {
	for _, f := range []string{"README.md", "INSTALL.md", "docs/playbook.md"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), strings.TrimRight(playbook.Rule, "\n")) {
			t.Errorf("%s does not carry the rule as playbook.Rule has it", f)
		}
		if f != "docs/playbook.md" && !strings.Contains(string(b), "docs/playbook.md") {
			t.Errorf("%s does not point at docs/playbook.md", f)
		}
	}
}
