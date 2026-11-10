// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package know

import "fmt"

// Meter is the context line for one prompt: how much is used, how much is left, where the sleep is. At or past the sleep
// share it opens with SLEEP NOW and what to do. "" when nothing is used yet.
func Meter(used, window int, sleep float64) string {
	if used <= 0 || window <= 0 {
		return ""
	}
	at := int(float64(window) * sleep)
	line := fmt.Sprintf("context: %dk of ~%dk (%.0f%%), ~%dk left; sleep at %.0f%% (~%dk).",
		used/1000, window/1000, 100*float64(used)/float64(window), max(window-used, 0)/1000, sleep*100, at/1000)
	if used >= at {
		line = "SLEEP NOW, " + line + " Before anything else: kv_write the pointers (what you are doing, what is open, what waits on the user) and the conclusions not yet filed, then go on."
	}
	return line
}
