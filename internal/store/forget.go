// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"time"
)

// Forget deletes the exchanges and hook records of one session, or of everything before a time (either may be empty)
// and returns the ids of the exchanges removed, so the caller can delete their vectors too. The ingest cursors stay, so
// the transcripts are not read in again; an exchange still growing in a live session comes back with its next line.
func (s *Store) Forget(ctx context.Context, session string, before time.Time) (ids []int64, events int64, err error) {
	cond, args := `session = ?`, []any{session}
	if session == "" {
		cond, args = `time < ?`, []any{stamp(before)}
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM exchanges WHERE `+cond, args...)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if _, err := tx.ExecContext(ctx, `DELETE FROM exchanges WHERE `+cond, args...); err != nil {
		return nil, 0, err
	}
	evArgs := args
	if session == "" {
		evArgs = []any{before.UTC().Format(eventStamp)}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM events WHERE `+cond, evArgs...)
	if err != nil {
		return nil, 0, err
	}
	events, _ = res.RowsAffected()
	return ids, events, tx.Commit()
}

// Export calls f with every exchange, its whole text included, oldest first; of one session when session is not empty.
func (s *Store) Export(ctx context.Context, session string, f func(Row) error) error {
	rows, err := s.r.QueryContext(ctx, `SELECT `+rowCols+`, e.text FROM exchanges e WHERE ?='' OR e.session=? ORDER BY e.time, e.id`, session, session)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var text string
		r, err := scanRow(rows, &text)
		if err != nil {
			return err
		}
		r.Text = text
		if err := f(r); err != nil {
			return err
		}
	}
	return rows.Err()
}
