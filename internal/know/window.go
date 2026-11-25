// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package know

import "strings"

// WindowOf returns the known context window for a model, or 0 for an unknown model.
// Unknown models fall back to the smaller default: overstating use makes the session sleep early, the safe direction.
func WindowOf(model string) int {
	if strings.HasSuffix(model, "[1m]") {
		return 1_000_000
	}
	if i := strings.IndexByte(model, '['); i >= 0 {
		model = model[:i]
	}
	for _, family := range []string{"claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8", "claude-opus-5", "claude-sonnet-4-6", "claude-sonnet-5", "claude-haiku-5", "claude-fable-", "claude-mythos-"} {
		if strings.Contains(model, family) {
			return 1_000_000
		}
	}
	return 0
}
