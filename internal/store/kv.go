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

// The kv table is the session's own notes:
// one short line per key, a pointer into the other stores (chat:<id>, file:line, sha), never their payload.
// A write appends a row and never changes one; a read takes each key's last row, so the history stays and a read can
// ask how the notes stood at a time, for example before a compaction.
//
// A note belongs to a project (the column project; '' is global, seen by every project). A key written under "global/" is
// global, and its stored key drops that prefix. A read for a project sees its own notes and the global ones; where both
// hold a key the project's own wins, a deletion of it included, so a project can mute a global note. "*" reads every project.

const (
	// KVSoft is the length a value should stay under: past it the write is kept and a warning names the key.
	KVSoft = 200
	// KVHard is the length no value may pass: the whole write is refused.
	KVHard = 500
	// KVDepth is how many segments, split by "/", a key may have: the depth of the notes tree.
	KVDepth  = 5
	kvKeyMax = 128
	// kvStamp has a fixed width, so the stamps compare as text in time order; RFC3339Nano drops trailing zeros and would not.
	kvStamp = "2006-01-02T15:04:05.000000000Z"
	// GlobalPrefix on a key written or read means the global scope; GlobalProject names that scope as a project.
	GlobalPrefix  = "global/"
	GlobalProject = "global"
	// AllProjects, as the project of a read, reads every project's notes.
	AllProjects = "*"
)

// ErrRefused marks a request the store will not take, as opposed to one it failed to carry out.
var ErrRefused = errors.New("refused")

// KV is one key's last value. Age and Stale are set by KVRead; Out and In only when the read follows edges.
type KV struct {
	Key     string    `json:"key"`
	Value   string    `json:"value"`
	Project string    `json:"project,omitempty"` // global, or the project's key as people read it
	Time    time.Time `json:"time"`
	Age     string    `json:"age,omitempty"`
	Stale   bool      `json:"stale,omitempty"`
	Out     []Edge    `json:"out,omitempty"`
	In      []Edge    `json:"in,omitempty"`
}

func refuse(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrRefused}, a...)...)
}

func kvKey(k string) error {
	switch {
	case k == "" || len(k) > kvKeyMax:
		return refuse("key %q: 1 to %d bytes", k, kvKeyMax)
	case strings.ContainsAny(k, " \t\r\n"):
		return refuse("key %q: no spaces", k)
	case strings.Count(k, "/") >= KVDepth:
		return refuse("key %q: at most %d segments", k, KVDepth)
	case slices.Contains(strings.Split(k, "/"), ""):
		return refuse("key %q: an empty segment", k)
	}
	return nil
}

func kvValue(k, v string) error {
	switch {
	case v == "":
		return refuse("key %q: an empty value; a key goes with del", k)
	case len(v) > KVHard:
		return refuse("key %q: %d bytes, over %d: keep a pointer (am:, chat:, file:line, sha), not the payload", k, len(v), KVHard)
	case strings.ContainsAny(v, "\r\n"):
		return refuse("key %q: one line", k)
	}
	return nil
}

// scoped splits a key as a caller wrote it: the project column it lives under (project is the caller's own, already a key,
// or GlobalProject) and the key as stored.
func scoped(project, key string) (col, stored string) {
	if k, ok := strings.CutPrefix(key, GlobalPrefix); ok {
		return "", k
	}
	return colOf(project), key
}

// colOf is the value of the project column for a project key: global is stored as ”.
func colOf(project string) string {
	if project == GlobalProject {
		return ""
	}
	return project
}

// label is a project column as a reader sees it.
func label(col string) string {
	if col == "" {
		return GlobalProject
	}
	return shortKey(col)
}

// writeScope resolves the project of a write: one project, never "*".
func (s *Store) writeScope(ctx context.Context, project string) (string, error) {
	if project == AllProjects {
		return "", refuse(`a write names one project, not "*"`)
	}
	return s.ResolveProject(ctx, project)
}

// KVWrite sets and deletes keys of a project in one transaction, after checking all of them: one bad entry writes nothing.
// It returns a warning for each value longer than KVSoft.
func (s *Store) KVWrite(ctx context.Context, project string, set map[string]string, del []string) ([]string, error) {
	if len(set)+len(del) == 0 {
		return nil, refuse("nothing to write")
	}
	project, err := s.writeScope(ctx, project)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var warn []string
	for _, k := range keys {
		_, stored := scoped(project, k)
		if err := errors.Join(kvKey(stored), kvValue(k, set[k])); err != nil {
			return nil, err
		}
		if n := len(set[k]); n > KVSoft {
			warn = append(warn, fmt.Sprintf("%s: %d bytes, over %d: split it, or point to where it lies", k, n, KVSoft))
		}
	}
	setScopes := map[string]bool{} // where each set key lands: the same key set and deleted under two spellings is still both
	for k := range set {
		setScopes[colOf2(project, k)] = true
	}
	for _, k := range del {
		if _, stored := scoped(project, k); kvKey(stored) != nil {
			return nil, kvKey(stored)
		}
		if setScopes[colOf2(project, k)] {
			return nil, refuse("key %q: both set and deleted", k)
		}
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(kvStamp)
	for _, k := range keys {
		col, stored := scoped(project, k)
		if _, err := tx.ExecContext(ctx, `INSERT INTO kv(key, value, time, deleted, project) VALUES(?,?,?,0,?)`, stored, set[k], now, col); err != nil {
			return nil, err
		}
	}
	for _, k := range del {
		col, stored := scoped(project, k)
		if _, err := tx.ExecContext(ctx, `INSERT INTO kv(key, value, time, deleted, project) VALUES(?,'',?,1,?)`, stored, now, col); err != nil {
			return nil, err
		}
	}
	return warn, tx.Commit()
}

// kvSel is what a read names: exact keys and key prefixes.
type kvSel struct{ keys, prefixes []string }

func (k kvSel) empty() bool { return len(k.keys)+len(k.prefixes) == 0 }

// cond is the SQL condition on a key column and its arguments.
func (k kvSel) cond() (string, []any) {
	var cond []string
	var args []any
	if len(k.keys) > 0 {
		cond = append(cond, `key IN (`+strings.TrimSuffix(strings.Repeat("?,", len(k.keys)), ",")+`)`)
		for _, key := range k.keys {
			args = append(args, key)
		}
	}
	for _, p := range k.prefixes {
		cond = append(cond, `instr(key, ?) = 1`) // instr counts characters, so it holds for a prefix that is not ASCII; '' gives 1
		args = append(args, p)
	}
	return strings.Join(cond, " OR "), args
}

// KVRead returns, sorted by key, the last value of each named key and of every key starting with a prefix; the prefix ""
// is every key. A deleted key is left out. A non-zero at reads the notes as they stood then. A name written under global/
// reads the global notes only; any other is read for the project and, where it has none, for global.
func (s *Store) KVRead(ctx context.Context, project string, keys, prefixes []string, at time.Time) ([]KV, error) {
	if len(keys)+len(prefixes) == 0 {
		return nil, refuse(`name keys or prefixes; the prefix "" reads every key`)
	}
	project, err := s.ResolveProject(ctx, project)
	if err != nil {
		return nil, err
	}
	var plain, global kvSel
	for _, k := range keys {
		if g, ok := strings.CutPrefix(k, GlobalPrefix); ok {
			global.keys = append(global.keys, g)
		} else {
			plain.keys = append(plain.keys, k)
		}
	}
	for _, p := range prefixes {
		if g, ok := strings.CutPrefix(p, GlobalPrefix); ok {
			global.prefixes = append(global.prefixes, g)
		} else {
			plain.prefixes = append(plain.prefixes, p)
		}
	}
	out := []KV{} // no notes is an empty list, not null
	for _, q := range []struct {
		sel        kvSel
		globalOnly bool
	}{{plain, false}, {global, true}} {
		if q.sel.empty() {
			continue
		}
		got, err := s.kvLast(ctx, project, q.globalOnly, q.sel, at)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	slices.SortStableFunc(out, func(a, b KV) int { return strings.Compare(a.Key+"\x00"+a.Project, b.Key+"\x00"+b.Project) })
	return slices.CompactFunc(out, func(a, b KV) bool { return a.Key == b.Key && a.Project == b.Project }), nil
}

// kvLast reads each selected key's last row in the scope of project: its own and the global ones, the project's winning
// where both hold the key (a deletion wins too); globalOnly reads the global ones; "*" reads every project's, none winning.
func (s *Store) kvLast(ctx context.Context, project string, globalOnly bool, sel kvSel, at time.Time) ([]KV, error) {
	cond, args := sel.cond()
	in, pick := `1`, `1`
	switch {
	case globalOnly:
		in = `project = ''`
	case project == AllProjects:
	default:
		in = `project IN (?, '')`
		args = append(args, colOf(project))
		pick = `(l.project <> '' OR NOT EXISTS (SELECT 1 FROM last o WHERE o.key = l.key AND o.project <> ''))`
	}
	q := `WITH last AS (SELECT key, project, max(rid) AS rid FROM kv WHERE (` + cond + `) AND ` + in
	if !at.IsZero() {
		q += ` AND time <= ?`
		args = append(args, at.UTC().Format(kvStamp))
	}
	rows, err := s.r.QueryContext(ctx, q+` GROUP BY key, project)
		SELECT k.key, k.project, k.value, k.time FROM last l JOIN kv k ON k.rid = l.rid WHERE k.deleted = 0 AND `+pick+` ORDER BY k.key, k.project`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ref := at
	if ref.IsZero() {
		ref = time.Now()
	}
	var out []KV
	for rows.Next() {
		var e KV
		var t, col string
		if err := rows.Scan(&e.Key, &col, &e.Value, &t); err != nil {
			return nil, err
		}
		e.Project = label(col)
		e.Time, _ = time.Parse(kvStamp, t)
		e.Age = kvAge(ref.Sub(e.Time))
		e.Stale = kvLive(e.Key) && ref.Sub(e.Time) > KVStaleAfter
		out = append(out, e)
	}
	return out, rows.Err()
}

// A note is found by meaning as well as by key: each key's last value gets a vector of "key: value". The kv rows stay
// unchanged; kv_vec lists the rows that have one. An older version keeps its vector, and KVLatest drops it at search.

// latestLive is the rid of each key's last row in each project, when that row is not a delete.
const latestLive = `SELECT rid FROM kv WHERE rid IN (SELECT max(rid) FROM kv GROUP BY key, project) AND deleted = 0`

// KVPending returns up to n notes waiting for a vector, as "key: value".
func (s *Store) KVPending(ctx context.Context, n int) ([]Pending, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT rid, key || ': ' || value FROM kv WHERE rid IN (`+latestLive+`)
		AND rid NOT IN (SELECT rid FROM kv_vec) ORDER BY rid LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Pending
	for rows.Next() {
		var p Pending
		if err := rows.Scan(&p.ID, &p.Text); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// KVMarkEmbedded records that these notes have vectors.
func (s *Store) KVMarkEmbedded(ctx context.Context, items []Pending) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range items {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO kv_vec(rid) VALUES(?)`, p.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// KVLatest returns, by rid, those of rids that are still their key's last live value, start with prefix and are in the
// scope of project (its own and the global notes, its own winning; "*": every project's). A prefix under global/ keeps the
// global notes only.
func (s *Store) KVLatest(ctx context.Context, project string, rids []int64, prefix string) (map[int64]KV, error) {
	out := map[int64]KV{}
	for len(rids) > 0 { // a statement binds at most 32766 values
		n := min(len(rids), 500)
		got, err := s.kvLatest(ctx, project, rids[:n], prefix)
		if err != nil {
			return nil, err
		}
		for rid, e := range got {
			out[rid] = e
		}
		rids = rids[n:]
	}
	return out, nil
}

func (s *Store) kvLatest(ctx context.Context, project string, rids []int64, prefix string) (map[int64]KV, error) {
	out := map[int64]KV{}
	if len(rids) == 0 {
		return out, nil
	}
	project, err := s.ResolveProject(ctx, project)
	if err != nil {
		return nil, err
	}
	scope, args := `1`, []any{}
	switch p, globalOnly := strings.CutPrefix(prefix, GlobalPrefix); {
	case globalOnly:
		prefix, scope = p, `k.project = ''`
	case project == AllProjects:
	default:
		scope = `(k.project = ? OR (k.project = '' AND NOT EXISTS (SELECT 1 FROM kv o WHERE o.key = k.key AND o.project = ?)))`
		args = append(args, colOf(project), colOf(project))
	}
	args = append(args, prefix)
	for _, r := range rids {
		args = append(args, r)
	}
	rows, err := s.r.QueryContext(ctx, `SELECT k.rid, k.key, k.project, k.value, k.time FROM kv k WHERE `+scope+` AND instr(k.key, ?) = 1 AND k.rid IN (`+latestLive+`)
		AND k.rid IN (`+strings.TrimSuffix(strings.Repeat("?,", len(rids)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rid int64
		var e KV
		var t, col string
		if err := rows.Scan(&rid, &e.Key, &col, &e.Value, &t); err != nil {
			return nil, err
		}
		e.Project = label(col)
		e.Time, _ = time.Parse(kvStamp, t)
		out[rid] = e
	}
	return out, rows.Err()
}

// KVHas returns which of keys have ever been written in exactly this project's scope (global for a key under global/), a
// deleted one included: a key the user deleted is theirs to have deleted.
func (s *Store) KVHas(ctx context.Context, project string, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(keys) == 0 {
		return out, nil
	}
	project, err := s.writeScope(ctx, project)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		col, stored := scoped(project, k)
		var one int
		switch err := s.r.QueryRowContext(ctx, `SELECT 1 FROM kv WHERE key = ? AND project = ? LIMIT 1`, stored, col).Scan(&one); {
		case err == nil:
			out[k] = true
		case !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}
	}
	return out, nil
}

// colOf2 names where a key lands: its project column and its stored key.
func colOf2(project, key string) string {
	col, stored := scoped(project, key)
	return col + "\x00" + stored
}
