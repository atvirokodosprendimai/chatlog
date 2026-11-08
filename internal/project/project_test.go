// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package project

import "testing"

func TestOf(t *testing.T) {
	cases := []struct{ cwd, want string }{
		{"/u/code/webshop", "webshop"},
		{"/u/code/webshop/", "webshop"},
		{"/u/code/litekov2", "litekov2"},
		{"", "unknown"},
	}
	for _, c := range cases {
		if got := Of(c.cwd); got != c.want {
			t.Errorf("%q: %s, want %s", c.cwd, got, c.want)
		}
	}
}
