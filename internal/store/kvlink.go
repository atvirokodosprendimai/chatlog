// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Edges beside the notes: a relation written as text in a value can be found only by a search and read only one way.
// A link is a triple (s, p, o) with the time it was written as its valid_from. Like kv it is append-only: a delete is a
// tombstone row, so a read at a time sees the edges as they stood.

// KVPredicates is the fixed list a link's predicate must come from; a closed list keeps the edges queryable by name.
var KVPredicates = []string{"affects", "blocked_by", "fixed_by", "decided_by", "applies_to", "supersedes", "see_also"}

const (
	// KVStaleAfter is the age past which a key under a live prefix is marked stale: its state was said then, not now.
	KVStaleAfter = 12 * time.Hour
	kvObjectMax  = 256
)

// kvLivePrefixes hold state that goes old; other keys (rule/, fact/, known/) are true until changed.
var kvLivePrefixes = []string{"now/", "waits/"}

func kvLive(key string) bool {
	return slices.ContainsFunc(kvLivePrefixes, func(p string) bool { return strings.HasPrefix(key, p) })
}

// kvAge is a duration in one short unit: seconds under a minute, minutes under an hour, hours under two days, then days.
func kvAge(d time.Duration) string {
	switch d = max(d, 0); {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}

// Link is one edge: s is a key, o a key or a pointer (repo@0074db3, chat:<id>, file:line, a URL).
type Link struct {
	S string `json:"s"`
	P string `json:"p"`
	O string `json:"o"`
}

// Edge is a link seen from one of its ends: out of a key it is {p, o}, into a key {s, p}.
type Edge struct {
	S string `json:"s,omitempty"`
	P string `json:"p"`
	O string `json:"o,omitempty"`
}

func kvLinkShape(l Link) error {
	switch {
	case !slices.Contains(KVPredicates, l.P):
		return refuse("link %s %s %s: predicate not one of %s", l.S, l.P, l.O, strings.Join(KVPredicates, ", "))
	case l.O == "" || len(l.O) > kvObjectMax:
		return refuse("link %s %s %q: an object of 1 to %d bytes", l.S, l.P, l.O, kvObjectMax)
	case strings.ContainsAny(l.O, " \t\r\n"):
		return refuse("link %s %s %q: no spaces in the object", l.S, l.P, l.O)
	}
	return kvKey(l.S)
}

// KVLink adds and deletes links in one transaction, after checking all of them: one bad link writes nothing.
// An added link's subject must be a live key, so an edge never hangs from nothing; a delete needs only the shape.
func (s *Store) KVLink(ctx context.Context, add, del []Link) error {
	if len(add)+len(del) == 0 {
		return refuse("nothing to link")
	}
	for _, l := range append(slices.Clone(add), del...) {
		if err := kvLinkShape(l); err != nil {
			return err
		}
	}
	for _, l := range del {
		if slices.Contains(add, l) {
			return refuse("link %s %s %s: both added and deleted", l.S, l.P, l.O)
		}
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, l := range add {
		var deleted int
		err := tx.QueryRowContext(ctx, `SELECT deleted FROM kv WHERE key = ? ORDER BY rid DESC LIMIT 1`, l.S).Scan(&deleted)
		if err == sql.ErrNoRows || deleted == 1 {
			return refuse("link %s %s %s: %q is not a key; write it first", l.S, l.P, l.O, l.S)
		}
		if err != nil {
			return err
		}
	}
	now := time.Now().UTC().Format(kvStamp)
	for i, ls := range [][]Link{add, del} {
		for _, l := range ls {
			if _, err := tx.ExecContext(ctx, `INSERT INTO kv_link(s, p, o, time, deleted) VALUES(?,?,?,?,?)`, l.S, l.P, l.O, now, i); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// liveLinks returns the links standing at at (zero is now) whose subject or object, as cond names it, is one of vals.
func (s *Store) liveLinks(ctx context.Context, cond string, vals []string, at time.Time) ([]Link, error) {
	args := make([]any, 0, 2*len(vals)+1)
	in := strings.TrimSuffix(strings.Repeat("?,", len(vals)), ",")
	for range strings.Count(cond, "%s") {
		for _, v := range vals {
			args = append(args, v)
		}
	}
	q := `SELECT s, p, o FROM kv_link WHERE rid IN (SELECT max(rid) FROM kv_link WHERE (` + strings.ReplaceAll(cond, "%s", in) + `)`
	if !at.IsZero() {
		q += ` AND time <= ?`
		args = append(args, at.UTC().Format(kvStamp))
	}
	rows, err := s.r.QueryContext(ctx, q+` GROUP BY s, p, o) AND deleted = 0 ORDER BY s, p, o`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.S, &l.P, &l.O); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// KVEdges returns, one hop, the edges out of and into each of keys as they stood at at (zero is now).
func (s *Store) KVEdges(ctx context.Context, keys []string, at time.Time) (out, in map[string][]Edge, err error) {
	out, in = map[string][]Edge{}, map[string][]Edge{}
	if len(keys) == 0 {
		return out, in, nil
	}
	links, err := s.liveLinks(ctx, `s IN (%s) OR o IN (%s)`, keys, at)
	if err != nil {
		return nil, nil, err
	}
	for _, l := range links {
		if slices.Contains(keys, l.S) {
			out[l.S] = append(out[l.S], Edge{P: l.P, O: l.O})
		}
		if slices.Contains(keys, l.O) {
			in[l.O] = append(in[l.O], Edge{S: l.S, P: l.P})
		}
	}
	return out, in, nil
}

// KVLinkedTo returns, sorted, the subjects of the links into o standing at at (zero is now).
func (s *Store) KVLinkedTo(ctx context.Context, o string, at time.Time) ([]string, error) {
	links, err := s.liveLinks(ctx, `o IN (%s)`, []string{o}, at)
	if err != nil {
		return nil, err
	}
	var subj []string
	for _, l := range links {
		subj = append(subj, l.S)
	}
	slices.Sort(subj)
	return slices.Compact(subj), nil
}
