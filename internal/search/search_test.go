// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package search

import (
	"reflect"
	"strings"
	"testing"
)

func TestFuse(t *testing.T) {
	if got := Fuse([]int64{1, 2, 3}, []int64{3, 4}); !reflect.DeepEqual(got, []int64{3, 1, 2, 4}) {
		t.Fatalf("%v", got)
	}
	if !reflect.DeepEqual(Fuse([]int64{5, 6}, nil), []int64{5, 6}) {
		t.Fatal("one list keeps its order")
	}
	// 8 scores 2/(k+2); 7 and 9 score 1/(k+1)+1/(k+3), larger by convexity; their tie breaks by id.
	if got := Fuse([]int64{7, 8, 9}, []int64{9, 8, 7}); !reflect.DeepEqual(got, []int64{7, 9, 8}) {
		t.Fatalf("%v", got)
	}
}

func TestFuseBothListsBeatOne(t *testing.T) {
	got := Fuse([]int64{10, 20}, []int64{30, 20})
	if got[0] != 20 {
		t.Fatalf("%v", got)
	}
}

func TestWindowFindsTheDensestPlace(t *testing.T) {
	text := strings.Repeat("noise ", 5000) + "the cmdtest guard failed here because cmdtest " + strings.Repeat("noise ", 5000) + "guard"
	got := Window(text, []string{"cmdtest", "guard"}, 200)
	if !strings.Contains(got, "cmdtest guard failed") || len(got) > 200 {
		t.Fatalf("%q", got)
	}
	if Window("short", []string{"absent"}, 200) != "" {
		t.Fatal("no term, no window")
	}
}

func TestRerankTextForms(t *testing.T) {
	c := candidate{words: "kur TEI", answer: "TEI ten", embed: "EMBEDTEXT", text: strings.Repeat("x ", 9000) + "bge endpoint"}
	if got := rerankText(FormHead, c, []string{"endpoint"}); !strings.HasPrefix(got, "EMBED") {
		t.Fatal(got)
	}
	if got := rerankText(FormWordsAnswer, c, nil); !strings.Contains(got, "kur TEI") || !strings.Contains(got, "TEI ten") {
		t.Fatal(got)
	}
	got := rerankText(FormWindow, c, []string{"endpoint"})
	if !strings.Contains(got, "kur TEI") || !strings.Contains(got, "bge endpoint") || len(got) > maxRerankBytes {
		t.Fatalf("%d", len(got))
	}
}
