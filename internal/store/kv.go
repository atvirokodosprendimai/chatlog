// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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
	// KVBodyMax is the size of a body, the long text that may go with a head (the `set` value): over it the whole write is refused.
	KVBodyMax = 8192
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
// ExpiresAt is the time the writer said the note goes old (optional, any key). DeletedAt is set only on a read with
// deleted: the note was deleted then, and Value is the last value it had.
type KV struct {
	Key       string     `json:"key"`
	Value     string     `json:"value,omitempty"`    // empty only in a keys_only read: a stored value is never empty
	Bytes     int        `json:"bytes,omitempty"`    // keys_only: the size of the value (and body), which that read leaves out
	Body      string     `json:"body,omitempty"`     // only when the read asked for full
	HasBody   bool       `json:"has_body,omitempty"` // the note has a body, returned or not
	BodyLen   int        `json:"-"`                  // its size in bytes, known whether or not it is returned
	Project   string     `json:"project,omitempty"`  // global, or the project's key as people read it
	Time      time.Time  `json:"time"`
	Age       string     `json:"age,omitempty"`
	Stale     bool       `json:"stale,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	Out       []Edge     `json:"out,omitempty"`
	In        []Edge     `json:"in,omitempty"`
}

// mark sets Age, Stale and HasBody as of ref. A note with an expiry is stale once it has passed; one without follows its
// prefix (now/ and waits/ go old after KVStaleAfter). A deleted note is not stale: it is history.
func (e *KV) mark(ref time.Time, expires string) {
	e.HasBody = e.BodyLen > 0
	e.Age = kvAge(ref.Sub(e.Time))
	if expires != "" {
		if t, err := time.Parse(kvStamp, expires); err == nil {
			e.ExpiresAt = &t
		}
	}
	if e.DeletedAt != nil {
		return
	}
	if e.ExpiresAt != nil {
		e.Stale = ref.After(*e.ExpiresAt)
		return
	}
	e.Stale = kvLive(e.Key) && ref.Sub(e.Time) > KVStaleAfter
}

// kvSpanDays bounds a span in days: 100 years, far under the 292 a time.Duration holds, so a huge number is refused, not wrapped.
const kvSpanDays = 36500

// ParseWhen reads a moment given as a span ("12h", "7d") from now, forward or back, or as an RFC3339 time. A span back from
// now is positive and at most kvSpanDays; going forward a span of zero or less parses and the caller says it is not in the future.
func ParseWhen(v string, now time.Time, forward bool) (time.Time, error) {
	bad := fmt.Errorf("%q is not a span (12h, 7d) or an RFC3339 time", v)
	var span time.Duration
	if d, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return time.Time{}, bad
		}
		if n > kvSpanDays || n < -kvSpanDays {
			return time.Time{}, fmt.Errorf("%q is over %d days", v, kvSpanDays)
		}
		span = time.Duration(n) * 24 * time.Hour
	} else if dur, err := time.ParseDuration(v); err == nil {
		span = dur
	} else if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	} else {
		return time.Time{}, bad
	}
	if forward {
		return now.Add(span), nil
	}
	if span <= 0 {
		return time.Time{}, fmt.Errorf("%q: a span back from now is positive", v)
	}
	return now.Add(-span), nil
}

// Changed is when the key last changed: its last write, or its delete.
func (e KV) Changed() time.Time {
	if e.DeletedAt != nil {
		return *e.DeletedAt
	}
	return e.Time
}

// kvExpiry reads an expiry as a span from now ("12h", "7d") or as an RFC3339 time, and refuses one not in the future.
func kvExpiry(k, v string, now time.Time) (string, error) {
	t, err := ParseWhen(v, now, true)
	if err != nil {
		return "", refuse("expires.%s: %s", k, err)
	}
	if !t.After(now) {
		return "", refuse("expires.%s: %q is not in the future", k, v)
	}
	return t.UTC().Format(kvStamp), nil
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
	return s.KVWritePlan(ctx, KVPlan{Project: project, Set: set, Del: del})
}

// KVPlan is one write into Project (a name, alias or key; "" is refused as everywhere). IfAbsent and IfValue are
// conditions checked in the write's own transaction, on each key in the scope it would be written to (a key under
// global/ is checked in the global scope): the write is refused, whole, unless every key in IfAbsent has no live value and
// every key in IfValue holds exactly the value named. Expires gives some set keys a time past which they are stale; Body
// gives some set keys a long text kept with the head.
type KVPlan struct {
	Project      string
	Set, Expires map[string]string
	Del          []string
	IfAbsent     []string
	IfValue      map[string]string
	Body         map[string]string
}

// KVWritePlan applies a plan in one transaction: after checking the entries and the conditions, all of it or nothing.
// It returns a warning for each value longer than KVSoft, and for each key whose body this write drops.
func (s *Store) KVWritePlan(ctx context.Context, p KVPlan) ([]string, error) {
	set, del := p.Set, p.Del
	if len(set)+len(del) == 0 {
		return nil, refuse("nothing to write")
	}
	project, err := s.writeScope(ctx, p.Project)
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
	for k, b := range p.Body {
		if _, ok := set[k]; !ok {
			return nil, refuse("body.%s: the key is not in set; a body goes with a head (the set value, what a reader and the search see)", k)
		}
		switch {
		case b == "":
			return nil, refuse("body.%s: empty; leave it out", k)
		case len(b) > KVBodyMax:
			return nil, refuse("body.%s: %d bytes, over %d: keep the rest where it lies and point to it", k, len(b), KVBodyMax)
		case !utf8.ValidString(b) || strings.ContainsRune(b, 0):
			return nil, refuse("body.%s: not valid text (UTF-8, no NUL)", k)
		}
	}
	exp := map[string]string{}
	for k, v := range p.Expires {
		if _, ok := set[k]; !ok {
			return nil, refuse("expires.%s: the key is not in set", k)
		}
		stamp, err := kvExpiry(k, v, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		exp[k] = stamp
	}
	for _, k := range p.IfAbsent {
		if _, stored := scoped(project, k); kvKey(stored) != nil {
			return nil, fmt.Errorf("if_absent: %w", kvKey(stored))
		}
	}
	for k, v := range p.IfValue {
		if _, stored := scoped(project, k); kvKey(stored) != nil {
			return nil, fmt.Errorf("if_value: %w", kvKey(stored))
		}
		if v == "" {
			return nil, refuse("if_value.%s: the expected value is empty; a key that must not exist goes in if_absent", k)
		}
	}
	tx, err := s.w.BeginTx(ctx, nil) // IMMEDIATE (see OpenWith): the conditions below and the inserts are one step, also against another process
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := s.kvConditions(ctx, tx, project, p); err != nil {
		return nil, err
	}
	// A write replaces the whole note, so a key that had a body and is written without one loses it from every read (the
	// history keeps it): say so, instead of dropping it silently.
	for _, k := range keys {
		if _, has := p.Body[k]; has {
			continue
		}
		col, stored := scoped(project, k)
		var n int
		err := tx.QueryRowContext(ctx, `SELECT CASE WHEN deleted = 0 THEN length(CAST(body AS BLOB)) ELSE 0 END FROM kv WHERE key = ? AND project = ? ORDER BY rid DESC LIMIT 1`, stored, col).Scan(&n)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if n > 0 {
			warn = append(warn, fmt.Sprintf("%s: it had a body (%d bytes) and this write has none: the body is gone from reads, kept in the history (kv_read at)", k, n))
		}
	}
	now := time.Now().UTC().Format(kvStamp)
	for _, k := range keys {
		col, stored := scoped(project, k)
		if _, err := tx.ExecContext(ctx, `INSERT INTO kv(key, value, time, deleted, project, expires, body) VALUES(?,?,?,0,?,?,?)`, stored, set[k], now, col, exp[k], p.Body[k]); err != nil {
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

// kvHeld is a key's last row in one scope as a refusal names it.
type kvHeld struct {
	found, live      bool
	value, at, whose string
}

// by says when and in which project the row was written ("deleted", for a delete).
func (r kvHeld) by(verb string) string {
	return fmt.Sprintf("%s %s in project %s", verb, r.at, r.whose)
}

// kvConditions checks IfAbsent and IfValue against each key's last row in its scope, inside tx (the writer's own
// transaction: a parallel claim cannot pass between the check and the insert), and names every key that fails with its
// current value, time and project (the scope the row lives in: the caller's own, or global).
func (s *Store) kvConditions(ctx context.Context, tx *sql.Tx, project string, p KVPlan) error {
	var fails []string
	current := func(k string) (kvHeld, error) {
		col, stored := scoped(project, k)
		var r kvHeld
		var del int
		err := tx.QueryRowContext(ctx, `SELECT value, time, deleted FROM kv WHERE key = ? AND project = ? ORDER BY rid DESC LIMIT 1`, stored, col).Scan(&r.value, &r.at, &del)
		if errors.Is(err, sql.ErrNoRows) {
			return r, nil
		}
		r.whose = s.projectName(ctx, col)
		r.found, r.live = true, del == 0
		return r, err
	}
	for _, k := range slices.Sorted(slices.Values(p.IfAbsent)) {
		r, err := current(k)
		if err != nil {
			return err
		}
		if r.live {
			fails = append(fails, fmt.Sprintf("if_absent %q: exists, value %q, %s", k, r.value, r.by("written")))
		}
	}
	for _, k := range slices.Sorted(maps.Keys(p.IfValue)) {
		r, err := current(k)
		if err != nil {
			return err
		}
		switch {
		case !r.found:
			fails = append(fails, fmt.Sprintf("if_value %q: expected %q, but the key was never written", k, p.IfValue[k]))
		case !r.live:
			fails = append(fails, fmt.Sprintf("if_value %q: expected %q, but the key was %s", k, p.IfValue[k], r.by("deleted")))
		case r.value != p.IfValue[k]:
			fails = append(fails, fmt.Sprintf("if_value %q: expected %q, current value %q, %s", k, p.IfValue[k], r.value, r.by("written")))
		}
	}
	if len(fails) > 0 {
		return refuse("condition not met, nothing written: %s", strings.Join(fails, "; "))
	}
	return nil
}

// projectName shows a project column as people read it: global, or [short key] and its first alias.
func (s *Store) projectName(ctx context.Context, col string) string {
	if col == "" {
		return GlobalProject
	}
	if al, err := s.Aliases(ctx, col); err == nil && len(al) > 0 {
		return shortKey(col) + " " + al[0] // shortKey is already [abc1234]
	}
	return shortKey(col)
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
	return s.KVReadFull(ctx, project, keys, prefixes, at, false, false)
}

// KVReadFull is KVRead that can also return the deleted keys (withDeleted: each with the last value it had and DeletedAt,
// so a fact that was true once can still be found) and each note's body (full). Without full the body text is not selected,
// only its size, so HasBody is known; the size is read from the row, which holds the body, so this saves the transfer,
// not the page read.
func (s *Store) KVReadFull(ctx context.Context, project string, keys, prefixes []string, at time.Time, withDeleted, full bool) ([]KV, error) {
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
		got, err := s.kvLast(ctx, project, q.globalOnly, q.sel, at, withDeleted, full)
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
// A key whose last row is a delete is left out, or with withDeleted returned at the last value it had.
func (s *Store) kvLast(ctx context.Context, project string, globalOnly bool, sel kvSel, at time.Time, withDeleted, full bool) ([]KV, error) {
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
	live := ` AND k.deleted = 0`
	if withDeleted {
		live = ``
	}
	rows, err := s.r.QueryContext(ctx, q+` GROUP BY key, project)
		SELECT k.rid, k.key, k.project, k.value, k.time, k.expires, k.deleted, length(CAST(k.body AS BLOB)), CASE WHEN ? THEN k.body ELSE '' END
		FROM last l JOIN kv k ON k.rid = l.rid WHERE `+pick+live+` ORDER BY k.key, k.project`, append(args, full)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ref := at
	if ref.IsZero() {
		ref = time.Now()
	}
	type tomb struct {
		i   int
		rid int64
		col string
	}
	var tombs []tomb
	var out []KV
	for rows.Next() {
		var e KV
		var rid, del int64
		var t, col, exp string
		if err := rows.Scan(&rid, &e.Key, &col, &e.Value, &t, &exp, &del, &e.BodyLen, &e.Body); err != nil {
			return nil, err
		}
		e.Project = label(col)
		e.Time, _ = time.Parse(kvStamp, t)
		if del == 1 {
			dt := e.Time
			e.DeletedAt = &dt
			tombs = append(tombs, tomb{len(out), rid, col})
		}
		e.mark(ref, exp)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	// A tombstone row holds no value: take the last live row before it, not after `at` (the clock may have stepped back).
	cutoff := "9999-12-31T23:59:59.999999999Z" // no bound
	if !at.IsZero() {
		cutoff = at.UTC().Format(kvStamp)
	}
	drop := map[int]bool{}
	for _, tb := range tombs {
		var t, exp string
		err := s.r.QueryRowContext(ctx, `SELECT value, time, expires, length(CAST(body AS BLOB)), CASE WHEN ? THEN body ELSE '' END FROM kv
			WHERE key = ? AND project = ? AND deleted = 0 AND rid < ? AND time <= ? ORDER BY rid DESC LIMIT 1`, full, out[tb.i].Key, tb.col, tb.rid, cutoff).
			Scan(&out[tb.i].Value, &t, &exp, &out[tb.i].BodyLen, &out[tb.i].Body)
		if errors.Is(err, sql.ErrNoRows) {
			drop[tb.i] = true // a delete of a key that never had a value
			continue
		}
		if err != nil {
			return nil, err
		}
		out[tb.i].Time, _ = time.Parse(kvStamp, t)
		out[tb.i].mark(ref, exp)
	}
	if len(drop) > 0 {
		kept := out[:0]
		for i, e := range out {
			if !drop[i] {
				kept = append(kept, e)
			}
		}
		out = kept
	}
	return out, nil
}

// A note is found by meaning as well as by key: each key's last value gets a vector of "key: head" (the head is the set
// value; a body is not indexed). The kv rows stay unchanged; kv_vec lists the rows that have one. An older version keeps
// its vector, and KVLatest drops it at search.

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
// global notes only. With withDeleted a key whose last row is a delete counts too: its last live value, marked DeletedAt.
func (s *Store) KVLatest(ctx context.Context, project string, rids []int64, prefix string) (map[int64]KV, error) {
	return s.KVLatestWith(ctx, project, rids, prefix, false)
}

// KVLatestWith is KVLatest that can, with withDeleted, also return a key whose last row is a delete, at the last value it had.
func (s *Store) KVLatestWith(ctx context.Context, project string, rids []int64, prefix string, withDeleted bool) (map[int64]KV, error) {
	out := map[int64]KV{}
	for len(rids) > 0 { // a statement binds at most 32766 values
		n := min(len(rids), 500)
		got, err := s.kvLatest(ctx, project, rids[:n], prefix, withDeleted)
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

func (s *Store) kvLatest(ctx context.Context, project string, rids []int64, prefix string, withDeleted bool) (map[int64]KV, error) {
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
	last := latestLive
	if withDeleted {
		last = `SELECT max(rid) FROM kv WHERE deleted = 0 GROUP BY key, project`
	}
	for _, r := range rids {
		args = append(args, r)
	}
	rows, err := s.r.QueryContext(ctx, `SELECT k.rid, k.key, k.project, k.value, k.time, k.expires, length(CAST(k.body AS BLOB)),
		(SELECT d.time FROM kv d WHERE d.deleted = 1 AND d.rid = (SELECT max(rid) FROM kv m WHERE m.key = k.key AND m.project = k.project))
		FROM kv k WHERE `+scope+` AND instr(k.key, ?) = 1 AND k.rid IN (`+last+`)
		AND k.rid IN (`+strings.TrimSuffix(strings.Repeat("?,", len(rids)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	for rows.Next() {
		var rid int64
		var e KV
		var t, col, exp string
		var dt sql.NullString
		if err := rows.Scan(&rid, &e.Key, &col, &e.Value, &t, &exp, &e.BodyLen, &dt); err != nil {
			return nil, err
		}
		e.Project = label(col)
		e.Time, _ = time.Parse(kvStamp, t)
		if dt.Valid {
			d, _ := time.Parse(kvStamp, dt.String)
			e.DeletedAt = &d
		}
		e.mark(now, exp)
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
