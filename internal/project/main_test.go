// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package project

import (
	"os"
	"testing"

	"github.com/atvirokodosprendimai/chatlog/internal/testenv"
)

// TestMain runs the tests with HOME, CLAUDE_CONFIG_DIR, XDG_* and CHATLOG_* pointing at a throwaway dir.
func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }
