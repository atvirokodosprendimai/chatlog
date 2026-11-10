// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"database/sql"
	"errors"
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

// linkScope splits a link's subject as a caller wrote it, as scoped does a key; the object is a pointer and stays as written.
func linkScope(project string, l Link) (col string, stored Link) {
	col, l.S = scoped(project, l.S)
	return col, l
}

// KVLink adds and deletes links of a project in one transaction, after checking all of them: one bad link writes nothing.
// An added link's subject must be a live key in the link's scope (the project's, or the global notes), so an edge never
// hangs from nothing; a delete needs only the shape. A subject under global/ makes a global link.
func (s *Store) KVLink(ctx context.Context, project string, add, del []Link) error {
	if len(add)+len(del) == 0 {
		return refuse("nothing to link")
	}
	project, err := s.writeScope(ctx, project)
	if err != nil {
		return err
	}
	for _, l := range append(slices.Clone(add), del...) {
		_, st := linkScope(project, l)
		if err := kvLinkShape(st); err != nil {
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
		col, st := linkScope(project, l)
		if ok, err := liveKey(ctx, tx, col, st.S); err != nil {
			return err
		} else if !ok {
			return refuse("link %s %s %s: %q is not a key; write it first", l.S, l.P, l.O, l.S)
		}
	}
	now := time.Now().UTC().Format(kvStamp)
	for i, ls := range [][]Link{add, del} {
		for _, l := range ls {
			col, st := linkScope(project, l)
			if _, err := tx.ExecContext(ctx, `INSERT INTO kv_link(s, p, o, time, deleted, project) VALUES(?,?,?,?,?,?)`, st.S, st.P, st.O, now, i, col); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// liveKey says whether key is live in the scope col: the column's own last row decides when it has one (a deletion
// included), else the global notes' do.
func liveKey(ctx context.Context, tx *sql.Tx, col, key string) (bool, error) {
	scopes := []string{col}
	if col != "" {
		scopes = append(scopes, "")
	}
	for _, c := range scopes {
		var deleted int
		switch err := tx.QueryRowContext(ctx, `SELECT deleted FROM kv WHERE key = ? AND project = ? ORDER BY rid DESC LIMIT 1`, key, c).Scan(&deleted); {
		case err == nil:
			return deleted == 0, nil
		case !errors.Is(err, sql.ErrNoRows):
			return false, err
		}
	}
	return false, nil
}

// liveLinks returns the links standing at at (zero is now) whose subject or object, as cond names it, is one of vals, in
// the scope of project: its own and the global ones, the project's own row of a link winning (a deletion included); "*": all.
func (s *Store) liveLinks(ctx context.Context, project, cond string, vals []string, at time.Time) ([]Link, error) {
	args := make([]any, 0, 2*len(vals)+2)
	in := strings.TrimSuffix(strings.Repeat("?,", len(vals)), ",")
	for range strings.Count(cond, "%s") {
		for _, v := range vals {
			args = append(args, v)
		}
	}
	scope, pick := `1`, `1`
	if project != AllProjects {
		scope = `project IN (?, '')`
		args = append(args, colOf(project))
		pick = `(l.project <> '' OR NOT EXISTS (SELECT 1 FROM last o WHERE o.s = l.s AND o.p = l.p AND o.o = l.o AND o.project <> ''))`
	}
	q := `WITH last AS (SELECT s, p, o, project, max(rid) AS rid FROM kv_link WHERE (` + strings.ReplaceAll(cond, "%s", in) + `) AND ` + scope
	if !at.IsZero() {
		q += ` AND time <= ?`
		args = append(args, at.UTC().Format(kvStamp))
	}
	rows, err := s.r.QueryContext(ctx, q+` GROUP BY s, p, o, project)
		SELECT DISTINCT k.s, k.p, k.o FROM last l JOIN kv_link k ON k.rid = l.rid WHERE k.deleted = 0 AND `+pick+` ORDER BY k.s, k.p, k.o`, args...)
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

// KVEdges returns, one hop, the edges out of and into each of keys as they stood at at (zero is now), in the scope of project.
func (s *Store) KVEdges(ctx context.Context, project string, keys []string, at time.Time) (out, in map[string][]Edge, err error) {
	out, in = map[string][]Edge{}, map[string][]Edge{}
	if len(keys) == 0 {
		return out, in, nil
	}
	if project, err = s.ResolveProject(ctx, project); err != nil {
		return nil, nil, err
	}
	links, err := s.liveLinks(ctx, project, `s IN (%s) OR o IN (%s)`, keys, at)
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

// KVLinkedTo returns, sorted, the subjects of the links into o standing at at (zero is now), in the scope of project.
func (s *Store) KVLinkedTo(ctx context.Context, project, o string, at time.Time) ([]string, error) {
	project, err := s.ResolveProject(ctx, project)
	if err != nil {
		return nil, err
	}
	links, err := s.liveLinks(ctx, project, `o IN (%s)`, []string{o}, at)
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
