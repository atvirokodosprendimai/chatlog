// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package aam reads another memory's SQLite file (drawers of verbatim text, and the facts mined from them) and hands each
// drawer out as one document. It reads only what is facts: the drawers' text and the triples; every vector, closet and
// embedding table is ignored, since the importing side embeds in its own way. The file is opened read-only and immutable,
// and rows are streamed: a file of hundreds of megabytes is never loaded whole.
package aam

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // the pure Go driver, as in the store
)

// FactsWing is the wing of the documents made of facts that have no source drawer: the file names none for them.
const FactsWing = "facts"

// factsPerDoc is how many facts without a source one document holds: a few KB, which an embedder reads whole.
const factsPerDoc = 40

// Doc is one drawer as a document: its chunks joined, its facts and its end appended.
type Doc struct {
	ID    string // stable across runs: the drawer's own id, or facts:<source>:<n>
	Wing  string
	Room  string
	Topic string
	Text  string
	Filed time.Time
	Refs  []string // what links this document to others: ent:<entity>, src:<source file>, sup:<older>><newer>
	Facts int      // how many facts were appended
	Ended bool     // the drawer was ended (valid_to set): kept, never to be taken for current
}

// Stats counts what Read handed out.
type Stats struct {
	Drawers, Joined, Facts, LooseFacts, FactDocs, Ended, SkippedFacts int // Facts went into drawers, LooseFacts into documents of their own
}

// Reader is an open file.
type Reader struct{ db *sql.DB }

// Open opens the file read-only and immutable: it is never written, not even a journal beside it.
func Open(path string) (*Reader, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro&immutable=1&_pragma=query_only(1)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	r := &Reader{db: db}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('drawers', 'kg_triples', 'kg_entities')`).Scan(&n); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if n != 3 {
		db.Close()
		return nil, fmt.Errorf("%s has no drawers, kg_triples and kg_entities tables: not a file of this kind", path)
	}
	return r, nil
}

// Close closes the file.
func (r *Reader) Close() error { return r.db.Close() }

// Teams lists the teams that own drawers, with their drawer counts.
func (r *Reader) Teams(ctx context.Context) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT team_id, count(*) FROM drawers GROUP BY team_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var t string
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, rows.Err()
}

// Options narrows a read to one team and, when Wings is not empty, to those wings (named with or without the wing_ prefix).
type Options struct {
	Team  string
	Wings []string
}

type fact struct {
	line     string
	ents     []string
	source   string // source_file, for a fact with no drawer
	drawer   string
	extracts string
}

// Read hands out every head drawer of the team, in order of wing, filing time and id, then the facts that no drawer holds.
func (r *Reader) Read(ctx context.Context, opt Options, emit func(Doc) error) (Stats, error) {
	var st Stats
	have, err := r.drawerIDs(ctx, opt.Team)
	if err != nil {
		return st, err
	}
	facts, loose, err := r.facts(ctx, opt.Team, have)
	if err != nil {
		return st, err
	}
	sup, err := r.supersessions(ctx, opt.Team)
	if err != nil {
		return st, err
	}
	q := `SELECT id, wing, room, topic, source_file, content, filed_at, content_date, valid_to, superseded_by, ended_reason FROM drawers
		WHERE team_id = ? AND parent_id = ''`
	args := []any{opt.Team}
	if len(opt.Wings) > 0 {
		q += ` AND wing IN (` + strings.TrimSuffix(strings.Repeat("?,", 2*len(opt.Wings)), ",") + `)`
		for _, w := range opt.Wings {
			args = append(args, w, "wing_"+strings.TrimPrefix(w, "wing_"))
		}
	}
	rows, err := r.db.QueryContext(ctx, q+` ORDER BY wing, filed_at, id`, args...)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	chunks, err := r.db.PrepareContext(ctx, `SELECT id, content FROM drawers WHERE team_id = ? AND parent_id = ? ORDER BY chunk_index`)
	if err != nil {
		return st, err
	}
	defer chunks.Close()
	for rows.Next() {
		var id, wing, room, topic, srcFile, content, filed, date, validTo, supBy, reason string
		if err := rows.Scan(&id, &wing, &room, &topic, &srcFile, &content, &filed, &date, &validTo, &supBy, &reason); err != nil {
			return st, err
		}
		ids := []string{id}
		parts := []string{content}
		cr, err := chunks.QueryContext(ctx, opt.Team, id)
		if err != nil {
			return st, err
		}
		for cr.Next() {
			var cid, c string
			if err := cr.Scan(&cid, &c); err != nil {
				cr.Close()
				return st, err
			}
			ids, parts = append(ids, cid), append(parts, c)
		}
		if err := cr.Err(); err != nil {
			cr.Close()
			return st, err
		}
		cr.Close()
		d := Doc{ID: id, Wing: wing, Room: room, Topic: topic, Text: Join(parts), Filed: ParseTime(filed, date)}
		if len(parts) > 1 {
			st.Joined++
		}
		var lines []string
		for _, cid := range ids {
			for _, f := range facts[cid] {
				lines = append(lines, f.line)
				for _, e := range f.ents {
					d.Refs = append(d.Refs, "ent:"+e)
				}
			}
			delete(facts, cid)
		}
		if srcFile != "" {
			d.Refs = append(d.Refs, "src:"+srcFile)
		}
		d.Refs = append(d.Refs, sup[id]...)
		if len(lines) > 0 {
			d.Text += "\n\nfacts:\n" + strings.Join(lines, "\n")
			d.Facts = len(lines)
			st.Facts += len(lines)
		}
		if validTo != "" {
			d.Ended = true
			st.Ended++
			d.Text += "\n\n[ENDED " + validTo + ": " + cmpOr(reason, "no reason given")
			if supBy != "" {
				d.Text += "; superseded by " + supBy
			}
			d.Text += "]"
		}
		d.Refs = unique(d.Refs)
		st.Drawers++
		if err := emit(d); err != nil {
			return st, err
		}
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	// what is left in facts belongs to a drawer of another wing, and is not ours; loose facts have no drawer at all
	for _, fs := range facts {
		st.SkippedFacts += len(fs)
	}
	if len(opt.Wings) > 0 { // a read of some wings is not the place for what no wing owns
		return st, nil
	}
	return st, emitLoose(loose, &st, emit)
}

func emitLoose(loose []fact, st *Stats, emit func(Doc) error) error {
	bySource := map[string][]fact{}
	for _, f := range loose {
		bySource[f.source] = append(bySource[f.source], f)
	}
	sources := make([]string, 0, len(bySource))
	for s := range bySource {
		sources = append(sources, s)
	}
	sort.Strings(sources)
	for _, src := range sources {
		fs := bySource[src]
		for n := 0; n*factsPerDoc < len(fs); n++ {
			part := fs[n*factsPerDoc : min((n+1)*factsPerDoc, len(fs))]
			sum := sha256.Sum256([]byte(src))
			d := Doc{ID: fmt.Sprintf("facts:%s:%d", hex.EncodeToString(sum[:6]), n), Wing: FactsWing, Room: cmpOr(src, "no source"), Topic: fmt.Sprintf("part %d", n+1), Facts: len(part)}
			var lines []string
			for _, f := range part {
				lines = append(lines, f.line)
				for _, e := range f.ents {
					d.Refs = append(d.Refs, "ent:"+e)
				}
				if t := ParseTime(f.extracts); t.After(d.Filed) {
					d.Filed = t
				}
			}
			if src != "" {
				d.Refs = append(d.Refs, "src:"+src)
			}
			d.Text = "facts without a source drawer (" + cmpOr(src, "no source") + "):\n" + strings.Join(lines, "\n")
			d.Refs = unique(d.Refs)
			st.FactDocs++
			st.LooseFacts += len(part)
			if err := emit(d); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Reader) drawerIDs(ctx context.Context, team string) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM drawers WHERE team_id = ?`, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// facts reads every triple with the names of its entities: by the drawer it came from, or, when that is none (or gone),
// as loose ones. A triple is one line: subject predicate object (valid ...).
func (r *Reader) facts(ctx context.Context, team string, have map[string]bool) (map[string][]fact, []fact, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT t.source_drawer_id, t.source_file, t.subject, coalesce(s.name, t.subject), t.predicate, t.object, coalesce(o.name, t.object),
		t.valid_from, t.valid_to, t.ended_reason, t.extracted_at FROM kg_triples t
		LEFT JOIN kg_entities s ON s.team_id = t.team_id AND s.id = t.subject LEFT JOIN kg_entities o ON o.team_id = t.team_id AND o.id = t.object
		WHERE t.team_id = ? ORDER BY t.source_drawer_id, t.extracted_at, t.id`, team)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	by := map[string][]fact{}
	var loose []fact
	for rows.Next() {
		var drawer, file, sid, sname, pred, oid, oname, from, to, reason, at string
		if err := rows.Scan(&drawer, &file, &sid, &sname, &pred, &oid, &oname, &from, &to, &reason, &at); err != nil {
			return nil, nil, err
		}
		f := fact{line: Line(sname, pred, oname, from, to, reason), ents: []string{sid, oid}, source: file, drawer: drawer, extracts: at}
		if drawer != "" && have[drawer] {
			by[drawer] = append(by[drawer], f)
		} else {
			loose = append(loose, f)
		}
	}
	return by, loose, rows.Err()
}

// supersessions maps a drawer to the links it shares with the one it replaced or was replaced by: sup:<older>><newer> on both.
func (r *Reader) supersessions(ctx context.Context, team string) (map[string][]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, superseded_by FROM drawers WHERE team_id = ? AND superseded_by <> ''`, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var old, next string
		if err := rows.Scan(&old, &next); err != nil {
			return nil, err
		}
		ref := "sup:" + old + ">" + next
		out[old], out[next] = append(out[old], ref), append(out[next], ref)
	}
	return out, rows.Err()
}

// Join puts a drawer's chunks back together. A chunk repeats the end of the one before it, by 20 to about 330 bytes, or
// follows it exactly; the longest repeat of at least 20 bytes is dropped.
func Join(chunks []string) string {
	if len(chunks) == 0 {
		return ""
	}
	out := chunks[0]
	for _, c := range chunks[1:] {
		k := 0
		for n := min(len(out), len(c), 1500); n >= 20; n-- {
			if strings.HasSuffix(out, c[:n]) {
				k = n
				break
			}
		}
		out += c[k:]
	}
	return out
}

// Line is a fact as one line: subject predicate object, and when it held.
func Line(subject, predicate, object, from, to, reason string) string {
	s := subject + " " + predicate + " " + object
	switch {
	case from != "" && to != "":
		s += " (" + from + ".." + to + ")"
	case from != "":
		s += " (from " + from + ")"
	case to != "":
		s += " (until " + to + ")"
	}
	if reason != "" {
		s += " [ended: " + reason + "]"
	}
	return s
}

var timeLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05", "2006-01-02"}

// ParseTime reads a stamp of the file: with or without a zone (none is UTC), then the date the memory is about; zero if neither reads.
func ParseTime(stamps ...string) time.Time {
	for _, s := range stamps {
		for _, l := range timeLayouts {
			if t, err := time.ParseInLocation(l, strings.TrimSpace(s), time.UTC); err == nil {
				return t.UTC()
			}
		}
	}
	return time.Time{}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func unique(s []string) []string {
	slices.Sort(s)
	return slices.Compact(s)
}

// ErrNoTeam is returned by PickTeam when the file holds several teams and none was named.
var ErrNoTeam = errors.New("the file holds several teams: name one with --team")

// PickTeam is the named team, or the only one.
func PickTeam(teams map[string]int, named string) (string, error) {
	if named != "" {
		if _, ok := teams[named]; !ok {
			return "", fmt.Errorf("team %q has no drawers in the file (teams: %s)", named, teamList(teams))
		}
		return named, nil
	}
	if len(teams) == 1 {
		for t := range teams {
			return t, nil
		}
	}
	if len(teams) == 0 {
		return "", errors.New("the file holds no drawers")
	}
	return "", fmt.Errorf("%w (%s)", ErrNoTeam, teamList(teams))
}

func teamList(teams map[string]int) string {
	var out []string
	for t, n := range teams {
		out = append(out, fmt.Sprintf("%s: %d drawers", t, n))
	}
	slices.Sort(out)
	return strings.Join(out, ", ")
}
