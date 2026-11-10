// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
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
)

// ErrRefused marks a request the store will not take, as opposed to one it failed to carry out.
var ErrRefused = errors.New("refused")

// KV is one key's last value. Age and Stale are set by KVRead; Out and In only when the read follows edges.
type KV struct {
	Key   string    `json:"key"`
	Value string    `json:"value"`
	Time  time.Time `json:"time"`
	Age   string    `json:"age,omitempty"`
	Stale bool      `json:"stale,omitempty"`
	Out   []Edge    `json:"out,omitempty"`
	In    []Edge    `json:"in,omitempty"`
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

// KVWrite sets and deletes keys in one transaction, after checking all of them: one bad entry writes nothing.
// It returns a warning for each value longer than KVSoft.
func (s *Store) KVWrite(ctx context.Context, set map[string]string, del []string) ([]string, error) {
	if len(set)+len(del) == 0 {
		return nil, refuse("nothing to write")
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var warn []string
	for _, k := range keys {
		if err := errors.Join(kvKey(k), kvValue(k, set[k])); err != nil {
			return nil, err
		}
		if n := len(set[k]); n > KVSoft {
			warn = append(warn, fmt.Sprintf("%s: %d bytes, over %d: split it, or point to where it lies", k, n, KVSoft))
		}
	}
	for _, k := range del {
		if err := kvKey(k); err != nil {
			return nil, err
		}
		if _, ok := set[k]; ok {
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO kv(key, value, time, deleted) VALUES(?,?,?,0)`, k, set[k], now); err != nil {
			return nil, err
		}
	}
	for _, k := range del {
		if _, err := tx.ExecContext(ctx, `INSERT INTO kv(key, value, time, deleted) VALUES(?,'',?,1)`, k, now); err != nil {
			return nil, err
		}
	}
	return warn, tx.Commit()
}

// KVRead returns, sorted by key, the last value of each named key and of every key starting with a prefix; the prefix ""
// is every key. A deleted key is left out. A non-zero at reads the notes as they stood then.
func (s *Store) KVRead(ctx context.Context, keys, prefixes []string, at time.Time) ([]KV, error) {
	if len(keys)+len(prefixes) == 0 {
		return nil, refuse(`name keys or prefixes; the prefix "" reads every key`)
	}
	var cond []string
	var args []any
	if len(keys) > 0 {
		cond = append(cond, `key IN (`+strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")+`)`)
		for _, k := range keys {
			args = append(args, k)
		}
	}
	for _, p := range prefixes {
		cond = append(cond, `instr(key, ?) = 1`) // instr counts characters, so it holds for a prefix that is not ASCII; '' gives 1
		args = append(args, p)
	}
	q := `SELECT key, value, time FROM kv WHERE rid IN (SELECT max(rid) FROM kv WHERE (` + strings.Join(cond, " OR ") + `)`
	if !at.IsZero() {
		q += ` AND time <= ?`
		args = append(args, at.UTC().Format(kvStamp))
	}
	rows, err := s.r.QueryContext(ctx, q+` GROUP BY key) AND deleted = 0 ORDER BY key`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KV{} // no notes is an empty list, not null
	ref := at
	if ref.IsZero() {
		ref = time.Now()
	}
	for rows.Next() {
		var e KV
		var t string
		if err := rows.Scan(&e.Key, &e.Value, &t); err != nil {
			return nil, err
		}
		e.Time, _ = time.Parse(kvStamp, t)
		e.Age = kvAge(ref.Sub(e.Time))
		e.Stale = kvLive(e.Key) && ref.Sub(e.Time) > KVStaleAfter
		out = append(out, e)
	}
	return out, rows.Err()
}

// A note is found by meaning as well as by key: each key's last value gets a vector of "key: value". The kv rows stay
// unchanged; kv_vec lists the rows that have one. An older version keeps its vector, and KVLatest drops it at search.

// latestLive is the rid of each key's last row, when that row is not a delete.
const latestLive = `SELECT rid FROM kv WHERE rid IN (SELECT max(rid) FROM kv GROUP BY key) AND deleted = 0`

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

// KVLatest returns, by rid, those of rids that are still their key's last live value and start with prefix.
func (s *Store) KVLatest(ctx context.Context, rids []int64, prefix string) (map[int64]KV, error) {
	out := map[int64]KV{}
	if len(rids) == 0 {
		return out, nil
	}
	args := []any{prefix}
	for _, r := range rids {
		args = append(args, r)
	}
	rows, err := s.r.QueryContext(ctx, `SELECT rid, key, value, time FROM kv WHERE instr(key, ?) = 1 AND rid IN (`+latestLive+`)
		AND rid IN (`+strings.TrimSuffix(strings.Repeat("?,", len(rids)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rid int64
		var e KV
		var t string
		if err := rows.Scan(&rid, &e.Key, &e.Value, &t); err != nil {
			return nil, err
		}
		e.Time, _ = time.Parse(kvStamp, t)
		out[rid] = e
	}
	return out, rows.Err()
}

// KVHas returns which of keys have ever been written, a deleted one included: a key the user deleted is theirs to have deleted.
func (s *Store) KVHas(ctx context.Context, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(keys) == 0 {
		return out, nil
	}
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	rows, err := s.r.QueryContext(ctx, `SELECT DISTINCT key FROM kv WHERE key IN (`+strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}
