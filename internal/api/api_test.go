// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package api

import (
	"testing"
	"unicode/utf8"

	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

func TestPageLenBoundsTheRequestedLength(t *testing.T) {
	for in, want := range map[string]int{"": GetDefault, "100": 100, "0": GetDefault, "-5": GetDefault, "x": GetDefault, "999999": GetDefault} {
		if got := pageLen(in); got != want {
			t.Errorf("pageLen(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestPaginateReadsARangeOnRuneBoundaries(t *testing.T) {
	row := store.Row{Text: "ąčęėįšųū"} // two bytes per rune
	p := Paginate(row, 1, 3)           // starts and ends inside a rune
	if !utf8.ValidString(p.Text) {
		t.Fatalf("text %q is not valid UTF-8", p.Text)
	}
	if p.Offset != 2 || p.Text != "č" || p.NextOffset != 4 {
		t.Fatalf("got offset %d text %q next %d, want 2 %q 4", p.Offset, p.Text, p.NextOffset, "č")
	}
}

func TestPaginateCarriesTheTextOnce(t *testing.T) {
	row := store.Row{Words: "klausimas", Answer: "atsakymas", Text: "M: klausimas\n\nA: atsakymas"}
	if p := Paginate(row, 0, 5); p.Words != "" || p.Answer != "" {
		t.Fatalf("words %q answer %q beside the text: a range would carry the whole exchange", p.Words, p.Answer)
	}
}
