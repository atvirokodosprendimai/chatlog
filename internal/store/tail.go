// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"slices"
)

// Tailed is one exchange of a tail: the row and its whole text, cut at the page's end only when it alone is larger.
type Tailed struct {
	Row
	Text       string `json:"text"`
	TotalBytes int    `json:"total_bytes"` // when larger than len(text), chat_get pages the rest
}

// Tail returns a project's latest exchanges before the exchange before (0: from now), oldest first, until their texts fill
// budget bytes: what was said and done before a compaction or the end of a session, read back from the end. next is the
// before of the page behind it, 0 when nothing is left. The newest exchange always comes, cut to budget when it alone is larger.
func (s *Store) Tail(ctx context.Context, project string, before int64, budget int) (items []Tailed, next int64, err error) {
	q := `SELECT ` + rowCols + `, e.text FROM exchanges e WHERE e.project = ?`
	args := []any{project}
	if before > 0 {
		q += ` AND (e.time, e.id) < (SELECT time, id FROM exchanges WHERE id = ?)`
		args = append(args, before)
	}
	// rows are stepped one at a time, so the texts past the budget are never read
	rows, err := s.r.QueryContext(ctx, q+` ORDER BY e.time DESC, e.id DESC`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	used := 0
	for rows.Next() {
		var text string
		r, err := scanRow(rows, &text)
		if err != nil {
			return nil, 0, err
		}
		if len(items) > 0 && used+len(text) > budget {
			next = items[len(items)-1].ID
			break
		}
		t := Tailed{Row: r, Text: text, TotalBytes: len(text)}
		t.Words, t.Answer = "", "" // both are inside the text; sending them twice would spend the budget twice
		if len(text) > budget {
			end := budget
			for end > 0 && text[end]&0xC0 == 0x80 {
				end--
			}
			t.Text = text[:end]
		}
		used += len(t.Text)
		items = append(items, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	slices.Reverse(items)
	return items, next, nil
}

// Recent returns a project's latest n exchanges, oldest first, each text cut to its first textMax bytes (the head is
// where the user's words and the first tool calls are): what a digest needs, without reading megabytes of tool output.
func (s *Store) Recent(ctx context.Context, project string, n, textMax int) ([]Tailed, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+rowCols+`, substr(e.text, 1, ?), e.text_len FROM exchanges e WHERE e.project = ?
		ORDER BY e.time DESC, e.id DESC LIMIT ?`, textMax, project, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tailed
	for rows.Next() {
		var t Tailed
		r, err := scanRow(rows, &t.Text, &t.TotalBytes)
		if err != nil {
			return nil, err
		}
		t.Row = r
		out = append(out, t)
	}
	slices.Reverse(out)
	return out, rows.Err()
}
