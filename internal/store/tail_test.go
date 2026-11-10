// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func put(t *testing.T, s *Store, project string, minute int, text string) {
	t.Helper()
	at := time.Date(2026, 11, 2, 10, minute, 0, 0, time.UTC)
	r := Row{UUID: fmt.Sprintf("%s-%d", project, minute), Session: "s", Source: "f", Time: at, End: at, Project: project,
		Words: "w", Answer: "a", Sha: text, Text: text}
	if err := s.Apply(context.Background(), "f-"+r.UUID, []Row{r}, Cursor{}); err != nil {
		t.Fatal(err)
	}
}

func texts(items []Tailed) string {
	var b []string
	for _, i := range items {
		b = append(b, i.Text)
	}
	return strings.Join(b, ",")
}

func TestTailPagesBackFromTheEndOldestFirst(t *testing.T) {
	ctx, s := context.Background(), open(t)
	// inserted out of time order: the tail goes by time, not by id
	put(t, s, "webshop", 3, "c3")
	put(t, s, "webshop", 1, "a1")
	put(t, s, "webshop", 2, "b2")
	put(t, s, "webshop", 4, "d4")
	put(t, s, "other", 5, "x5")
	items, next, err := s.Tail(ctx, "webshop", "", 0, 4)
	if err != nil || texts(items) != "c3,d4" || next == 0 {
		t.Fatalf("the last two, oldest first: %q next %d %v", texts(items), next, err)
	}
	if items[0].Words != "" || items[0].TotalBytes != 2 {
		t.Fatalf("words are in the text, not sent twice: %+v", items[0])
	}
	items, next, err = s.Tail(ctx, "webshop", "", next, 4)
	if err != nil || texts(items) != "a1,b2" || next != 0 {
		t.Fatalf("the page behind, then nothing left: %q next %d %v", texts(items), next, err)
	}
}

func TestTailCutsAnExchangeLargerThanTheBudget(t *testing.T) {
	ctx, s := context.Background(), open(t)
	put(t, s, "webshop", 1, "ąąąą")
	items, _, err := s.Tail(ctx, "webshop", "", 0, 3)
	if err != nil || len(items) != 1 || items[0].Text != "ą" || items[0].TotalBytes != 8 {
		t.Fatalf("cut on a rune boundary, its size kept: %+v %v", items, err)
	}
}
