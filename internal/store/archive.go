// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// An archived exchange is one that is kept and found only when asked for: old, and maybe no longer true. Every read hides
// it unless it asks for the archive; the one way to change that is SetArchived, which restores one row and never its
// neighbours, so what still holds is brought back by someone who checked it.

// EventArchive is the row SetArchived writes to the events table: input = "id=N archived|restored: why", so `chatlog events`
// keeps who-did-what-when without a table of its own.
const EventArchive = "Archive"

// NeighbourMax is how many neighbours a restore names; the rest are counted.
const NeighbourMax = 10

// Neighbour is an archived row that shares a link with a restored one.
type Neighbour struct {
	ID   int64    `json:"id"`
	Line string   `json:"line"`
	Via  []string `json:"via"` // the links they share
}

// Restored is what SetArchived answers: whether the row changed, and, for a restore, the archived rows one hop from it.
type Restored struct {
	ID         int64       `json:"id"`
	Changed    bool        `json:"changed"`
	Neighbours []Neighbour `json:"neighbours,omitempty"`
	More       int         `json:"more_neighbours,omitempty"`
}

// SetArchived archives a row or restores it. A restore needs a why, kept in the events table with the time. It restores
// that row only; the answer lists the archived rows that share a link with it (the same source drawer, the same entity of a
// fact, a replaced version), up to NeighbourMax, best linked first, for the caller to read and restore one by one.
func (s *Store) SetArchived(ctx context.Context, id int64, archived bool, why string) (Restored, error) {
	why = strings.TrimSpace(why)
	if !archived && why == "" {
		return Restored{}, refuse("restoring an archived row needs a why: what you checked that says it still holds")
	}
	if len(why) > 500 {
		return Restored{}, refuse("why: at most 500 bytes")
	}
	out := Restored{ID: id}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var was bool
	switch err := tx.QueryRowContext(ctx, `SELECT archived FROM exchanges WHERE id = ?`, id).Scan(&was); {
	case errors.Is(err, sql.ErrNoRows):
		return out, refuse("no exchange %d", id)
	case err != nil:
		return out, err
	}
	if out.Changed = was != archived; out.Changed {
		if _, err := tx.ExecContext(ctx, `UPDATE exchanges SET archived = ? WHERE id = ?`, archived, id); err != nil {
			return out, err
		}
		verb := map[bool]string{true: "archived", false: "restored"}[archived]
		if _, err := tx.ExecContext(ctx, `INSERT INTO events(time, event, session, cwd, tool, tool_use_id, input, output) VALUES(?,?,'','','chat_archive','',?,'')`,
			time.Now().UTC().Format(eventStamp), EventArchive, fmt.Sprintf("id=%d %s: %s", id, verb, why)); err != nil {
			return out, err
		}
	}
	if err := tx.Commit(); err != nil || archived {
		return out, err
	}
	out.Neighbours, out.More, err = s.neighbours(ctx, id)
	return out, err
}

// neighbours lists the archived rows that share a ref with id, those sharing most first, and how many more there are.
func (s *Store) neighbours(ctx context.Context, id int64) ([]Neighbour, int, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT e.id, e.words, e.answer, group_concat(b.ref, char(10)), count(*) AS shared FROM row_ref a
		JOIN row_ref b ON b.ref = a.ref AND b.row <> a.row JOIN exchanges e ON e.id = b.row
		WHERE a.row = ? AND e.archived = 1 GROUP BY e.id ORDER BY shared DESC, e.id`, id)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Neighbour
	more := 0
	for rows.Next() {
		var n Neighbour
		var words, answer, via string
		var shared int
		if err := rows.Scan(&n.ID, &words, &answer, &via, &shared); err != nil {
			return nil, 0, err
		}
		if len(out) == NeighbourMax {
			more++
			continue
		}
		n.Line = oneLine(words + ": " + answer)
		n.Via = strings.Split(via, "\n")
		out = append(out, n)
	}
	return out, more, rows.Err()
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = strings.ToValidUTF8(s[:160], "") + "…"
	}
	return s
}
