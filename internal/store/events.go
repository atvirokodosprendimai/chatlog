// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// The events table is what the hooks record as it happens: each prompt, each tool call with its start and duration, and
// the boundaries of a session. Fields are already redacted and capped by the hook.
const eventsSchema = `
CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY, time TEXT NOT NULL, event TEXT NOT NULL, session TEXT NOT NULL,
  cwd TEXT NOT NULL, tool TEXT NOT NULL, tool_use_id TEXT NOT NULL, duration_ms INTEGER, input TEXT NOT NULL, output TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS events_session ON events(session, event, id);
CREATE INDEX IF NOT EXISTS events_tool_use ON events(tool_use_id) WHERE tool_use_id <> '';
`

// eventStamp has a fixed width, so stamps compare as text in time order, and keeps milliseconds for durations.
const eventStamp = "2006-01-02T15:04:05.000Z"

// eventCols are the columns a row is written and read with, in that order.
const eventCols = `time, event, session, cwd, tool, tool_use_id, duration_ms, input, output`

// Event is one hook record. Event is the hook's name (UserPromptSubmit, PreToolUse, SessionStart, ...); a tool call is
// one row opened by PreToolUse and closed by PostToolUse, which sets DurationMs and Output.
type Event struct {
	ID         int64     `json:"id"`
	Time       time.Time `json:"time"`
	Event      string    `json:"event"`
	Session    string    `json:"session"`
	Cwd        string    `json:"cwd,omitempty"`
	Tool       string    `json:"tool,omitempty"`
	ToolUseID  string    `json:"tool_use_id,omitempty"`
	DurationMs *int64    `json:"duration_ms,omitempty"`
	Input      string    `json:"input,omitempty"`
	Output     string    `json:"output,omitempty"`
}

// AddEvent appends one record.
func (s *Store) AddEvent(ctx context.Context, e Event) (int64, error) {
	res, err := s.w.ExecContext(ctx, `INSERT INTO events(`+eventCols+`) VALUES(?,?,?,?,?,?,?,?,?)`,
		e.Time.UTC().Format(eventStamp), e.Event, e.Session, e.Cwd, e.Tool, e.ToolUseID, e.DurationMs, e.Input, e.Output)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishTool closes the row PreToolUse opened for a call: its duration from the row's start to end, and its output.
// ok is false when no open row has that id (the hook was installed mid-call, or PreToolUse failed).
func (s *Store) FinishTool(ctx context.Context, toolUseID string, end time.Time, output string) (start time.Time, ok bool, err error) {
	if toolUseID == "" {
		return time.Time{}, false, nil // without an id any open row would match: none is the call's
	}
	tx, err := s.w.BeginTx(ctx, nil) // the writer pool begins IMMEDIATE: the read and the update hold one write lock
	if err != nil {
		return time.Time{}, false, err
	}
	defer tx.Rollback()
	var id int64
	var at string
	// tool_use_id <> '' lets SQLite use the partial index events_tool_use for a bound id
	err = tx.QueryRowContext(ctx, `SELECT id, time FROM events WHERE tool_use_id=? AND tool_use_id <> '' AND event='PreToolUse'
		AND duration_ms IS NULL ORDER BY id DESC LIMIT 1`, toolUseID).Scan(&id, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	start, err = time.Parse(eventStamp, at)
	if err != nil {
		return time.Time{}, false, err
	}
	ms := max(end.Sub(start).Milliseconds(), 0)
	if _, err := tx.ExecContext(ctx, `UPDATE events SET duration_ms=?, output=? WHERE id=?`, ms, output, id); err != nil {
		return time.Time{}, false, err
	}
	return start, true, tx.Commit()
}

// AdoptPre writes a PreToolUse that waited in a spool. When its PostToolUse already recorded the call alone (no open
// row to close then), the two become one row: the start and input of the PreToolUse, the duration between them, and
// the output of the PostToolUse. adopted is false when it was written as a new row.
func (s *Store) AdoptPre(ctx context.Context, e Event) (adopted bool, err error) {
	if e.ToolUseID != "" {
		var id int64
		var at string
		err := s.w.QueryRowContext(ctx, `SELECT id, time FROM events WHERE tool_use_id=? AND tool_use_id <> '' AND event='PostToolUse'
			ORDER BY id DESC LIMIT 1`, e.ToolUseID).Scan(&id, &at)
		if err == nil {
			end, perr := time.Parse(eventStamp, at)
			if perr != nil {
				return false, perr
			}
			ms := max(end.Sub(e.Time).Milliseconds(), 0)
			_, err = s.w.ExecContext(ctx, `UPDATE events SET event='PreToolUse', time=?, input=?, duration_ms=? WHERE id=?`,
				e.Time.UTC().Format(eventStamp), e.Input, ms, id)
			return err == nil, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	_, err = s.AddEvent(ctx, e)
	return false, err
}

// LastEvent is the time of a session's latest record of that event; ok is false when it has none.
func (s *Store) LastEvent(ctx context.Context, session, event string) (at time.Time, ok bool, err error) {
	var t string
	err = s.r.QueryRowContext(ctx, `SELECT time FROM events WHERE session=? AND event=? ORDER BY id DESC LIMIT 1`, session, event).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	at, err = time.Parse(eventStamp, t)
	return at, err == nil, err
}

// LastInput is the input of a session's latest record of that event; ok is false when it has none.
func (s *Store) LastInput(ctx context.Context, session, event string) (input string, ok bool, err error) {
	err = s.r.QueryRowContext(ctx, `SELECT input FROM events WHERE session=? AND event=? ORDER BY id DESC LIMIT 1`, session, event).Scan(&input)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return input, err == nil, err
}

// HasEvent says whether a session holds a record of that event with exactly that input.
func (s *Store) HasEvent(ctx context.Context, session, event, input string) (bool, error) {
	var one int
	err := s.r.QueryRowContext(ctx, `SELECT 1 FROM events WHERE session=? AND event=? AND input=? LIMIT 1`, session, event, input).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Events returns the latest n records, of one session when session is not empty, oldest first.
func (s *Store) Events(ctx context.Context, session string, n int) ([]Event, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT * FROM (SELECT id, `+eventCols+`
		FROM events WHERE ?='' OR session=? ORDER BY id DESC LIMIT ?) ORDER BY id`, session, session, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var t string
		var ms sql.NullInt64
		if err := rows.Scan(&e.ID, &t, &e.Event, &e.Session, &e.Cwd, &e.Tool, &e.ToolUseID, &ms, &e.Input, &e.Output); err != nil {
			return nil, err
		}
		e.Time, _ = time.Parse(eventStamp, t)
		if ms.Valid {
			e.DurationMs = &ms.Int64
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
