// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package store is the SQLite file: exchanges, their full text, full-text index and ingest cursors. One writer connection, a pool of
// readers (WAL), so a search never waits for an ingest. It sits behind this package so the driver can change.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite" // the pure Go driver; FTS5 is built in
)

const schema = `
CREATE TABLE IF NOT EXISTS exchanges(
  id INTEGER PRIMARY KEY, uuid TEXT NOT NULL UNIQUE, session TEXT NOT NULL, agent TEXT NOT NULL, seq INTEGER NOT NULL, source TEXT NOT NULL,
  time TEXT NOT NULL, end_time TEXT NOT NULL, cwd TEXT NOT NULL, project TEXT NOT NULL, prio INTEGER NOT NULL, closed INTEGER NOT NULL,
  cut_bytes INTEGER NOT NULL, over_limit INTEGER NOT NULL, text_len INTEGER NOT NULL, sha TEXT NOT NULL, embedded_sha TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL, words TEXT NOT NULL, answer TEXT NOT NULL, embed_text TEXT NOT NULL, text TEXT NOT NULL, pkey TEXT NOT NULL DEFAULT '', archived INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS exchanges_time ON exchanges(time);
CREATE INDEX IF NOT EXISTS exchanges_pending ON exchanges(prio, time DESC) WHERE embedded_sha <> sha;
CREATE VIRTUAL TABLE IF NOT EXISTS fts USING fts5(words, answer, text, content='exchanges', content_rowid='id', tokenize='unicode61 remove_diacritics 2');
CREATE TRIGGER IF NOT EXISTS ex_ai AFTER INSERT ON exchanges BEGIN
  INSERT INTO fts(rowid, words, answer, text) VALUES (new.id, new.words, new.answer, new.text); END;
CREATE TRIGGER IF NOT EXISTS ex_ad AFTER DELETE ON exchanges BEGIN
  INSERT INTO fts(fts, rowid, words, answer, text) VALUES ('delete', old.id, old.words, old.answer, old.text); END;
CREATE TRIGGER IF NOT EXISTS ex_au AFTER UPDATE OF words, answer, text ON exchanges BEGIN
  INSERT INTO fts(fts, rowid, words, answer, text) VALUES ('delete', old.id, old.words, old.answer, old.text);
  INSERT INTO fts(rowid, words, answer, text) VALUES (new.id, new.words, new.answer, new.text); END;
CREATE TABLE IF NOT EXISTS cursors(path TEXT PRIMARY KEY, offset INTEGER NOT NULL, read_to INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS kv(rid INTEGER PRIMARY KEY, key TEXT NOT NULL, value TEXT NOT NULL, time TEXT NOT NULL, deleted INTEGER NOT NULL, project TEXT NOT NULL DEFAULT '', expires TEXT NOT NULL DEFAULT '', body TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS kv_key ON kv(key, rid);
CREATE TABLE IF NOT EXISTS kv_vec(rid INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS kv_link(rid INTEGER PRIMARY KEY, s TEXT NOT NULL, p TEXT NOT NULL, o TEXT NOT NULL, time TEXT NOT NULL, deleted INTEGER NOT NULL, project TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS kv_link_s ON kv_link(s, rid);
CREATE INDEX IF NOT EXISTS kv_link_o ON kv_link(o, rid);
CREATE TABLE IF NOT EXISTS project_alias(alias TEXT PRIMARY KEY, key TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS project_alias_key ON project_alias(key);
CREATE TABLE IF NOT EXISTS project_root(root TEXT PRIMARY KEY, key TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS row_ref(row INTEGER NOT NULL, ref TEXT NOT NULL, PRIMARY KEY(row, ref)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS row_ref_ref ON row_ref(ref);
`

// Store holds the two connection pools.
type Store struct {
	w, r *sql.DB
}

// Open makes the file private before SQLite first touches it, so its -wal and -shm take the same mode.
func Open(path string) (*Store, error) { return OpenWith(path, 10*time.Second) }

// OpenWith is Open with the time a statement may wait for another process's lock: a hook waits less than the server.
func OpenWith(path string, busy time.Duration) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", path, busy.Milliseconds())
	// A deferred transaction that reads, then writes, fails at once with SQLITE_BUSY when another process wrote between;
	// IMMEDIATE takes the write lock at BEGIN, where busy_timeout waits for it. Only the writer pool writes.
	wdsn := dsn + "&_txlock=immediate"
	w, err := sql.Open("sqlite", wdsn)
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	if _, err := w.Exec(schema + eventsSchema); err != nil {
		w.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	if err := migrate(w); err != nil {
		w.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	r, err := sql.Open("sqlite", dsn+"&_pragma=query_only(1)")
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(8)
	r.SetMaxIdleConns(8) // closing a connection after each read is what costs: keep them
	return &Store{w: w, r: r}, nil
}

// addKVColumn adds a TEXT column with an empty default to kv. The caller checked that it is missing, and another process
// opening the same old file may have added it since: that is the state wanted, not a failure.
func addKVColumn(db *sql.DB, col string) error {
	_, err := db.Exec(`ALTER TABLE kv ADD COLUMN ` + col + ` TEXT NOT NULL DEFAULT ''`)
	if err != nil && strings.Contains(err.Error(), "duplicate column name") {
		return nil
	}
	return err
}

// Close closes both pools.
func (s *Store) Close() error { return errors.Join(s.r.Close(), s.w.Close()) }

// Row is one exchange as stored.
type Row struct {
	ID        int64     `json:"id"`
	UUID      string    `json:"uuid"`
	Session   string    `json:"session"`
	Agent     string    `json:"agent,omitempty"`
	Seq       int64     `json:"seq"` // the byte offset of the opening line: order within its file, free of state
	Source    string    `json:"source"`
	Time      time.Time `json:"time"`
	End       time.Time `json:"end"`
	Cwd       string    `json:"cwd"`
	Project   string    `json:"project"`
	PKey      string    `json:"pkey,omitempty"`     // the project's key: see package project; empty on an exchange stored before keys
	Archived  bool      `json:"archived,omitempty"` // hidden from every read unless it asks for the archive; see Archive
	Refs      []string  `json:"-"`                  // what links this row to others (see row_ref): rows that share one are neighbours
	Prio      int       `json:"-"`
	Closed    bool      `json:"closed"`
	CutBytes  int       `json:"cut_bytes"`
	OverLimit bool      `json:"over_limit"`
	Sha       string    `json:"sha"`
	Words     string    `json:"words"`
	Answer    string    `json:"answer"`
	Embed     string    `json:"-"`
	Text      string    `json:"-"`
}

// Cursor is how far a file was read: Offset is where the next read starts, ReadTo where this one ended.
type Cursor struct{ Offset, ReadTo int64 }

// CursorOf returns a file's cursor, zero when it was never read.
func (s *Store) CursorOf(ctx context.Context, path string) (Cursor, error) {
	var c Cursor
	err := s.r.QueryRowContext(ctx, `SELECT offset, read_to FROM cursors WHERE path=?`, path).Scan(&c.Offset, &c.ReadTo)
	if errors.Is(err, sql.ErrNoRows) {
		return Cursor{}, nil
	}
	return c, err
}

// Apply commits rows and the file's cursor in one transaction: an interruption doubles nothing and loses nothing.
// A row with a known uuid is replaced when it changed, unless it is a shorter copy (a resumed session repeats old rows).
func (s *Store) Apply(ctx context.Context, path string, rows []Row, c Cursor) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range rows {
		if err := upsert(ctx, tx, &rows[i], now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO cursors(path, offset, read_to) VALUES(?,?,?)
		ON CONFLICT(path) DO UPDATE SET offset=excluded.offset, read_to=excluded.read_to`, path, c.Offset, c.ReadTo); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES('last_ingest', ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, now); err != nil {
		return err
	}
	return tx.Commit()
}

func upsert(ctx context.Context, tx *sql.Tx, r *Row, now string) error {
	var id int64
	var sha string
	var textLen int
	err := tx.QueryRowContext(ctx, `SELECT id, sha, text_len FROM exchanges WHERE uuid=?`, r.UUID).Scan(&id, &sha, &textLen)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// archived is set here and nowhere after: a row someone restored is not archived again by the import that made it
		res, err := tx.ExecContext(ctx, `INSERT INTO exchanges(uuid, session, agent, seq, source, time, end_time, cwd, project, pkey, prio, closed, cut_bytes,
			over_limit, text_len, sha, updated_at, words, answer, embed_text, text, archived) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.UUID, r.Session, r.Agent, r.Seq, r.Source, stamp(r.Time), stamp(r.End), r.Cwd, r.Project, r.PKey, r.Prio, r.Closed, r.CutBytes,
			r.OverLimit, len(r.Text), r.Sha, now, r.Words, r.Answer, r.Embed, r.Text, r.Archived)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		if r.PKey != "" && validAlias(r.Project) == nil { // the folder's name is the first alias of its project
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_alias(alias, key) VALUES(?, ?)`, r.Project, r.PKey); err != nil {
				return err
			}
		}
	case err != nil:
		return err
	case sha == r.Sha || len(r.Text) < textLen:
		if _, err := tx.ExecContext(ctx, `UPDATE exchanges SET closed=max(closed, ?) WHERE uuid=?`, r.Closed, r.UUID); err != nil {
			return err
		}
	default:
		if _, err := tx.ExecContext(ctx, `UPDATE exchanges SET end_time=?, closed=?, cut_bytes=?, over_limit=?, text_len=?, sha=?, updated_at=?, words=?, answer=?, embed_text=?, text=? WHERE uuid=?`,
			stamp(r.End), r.Closed, r.CutBytes, r.OverLimit, len(r.Text), r.Sha, now, r.Words, r.Answer, r.Embed, r.Text, r.UUID); err != nil {
			return err
		}
	}
	for _, ref := range r.Refs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO row_ref(row, ref) VALUES(?, ?)`, id, ref); err != nil {
			return err
		}
	}
	return nil
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func parse(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// Pending is an exchange waiting for a vector.
type Pending struct {
	ID        int64
	Sha, Text string
}

// PendingEmbeds returns what needs a vector, newest first. An exchange still growing waits until it is quiet, or
// its vector would be redone at every new line; quietBefore zero takes them all.
func (s *Store) PendingEmbeds(ctx context.Context, n int, quietBefore time.Time) ([]Pending, error) {
	q := `SELECT id, sha, embed_text FROM exchanges WHERE embedded_sha <> sha`
	args := []any{}
	if !quietBefore.IsZero() {
		q += ` AND (closed=1 OR updated_at < ?)`
		args = append(args, stamp(quietBefore))
	}
	rows, err := s.r.QueryContext(ctx, q+` ORDER BY prio, time DESC LIMIT ?`, append(args, n)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Pending
	for rows.Next() {
		var p Pending
		if err := rows.Scan(&p.ID, &p.Sha, &p.Text); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MarkEmbedded records vectors. The sha guard keeps an exchange pending if it changed while its vector was being made.
func (s *Store) MarkEmbedded(ctx context.Context, items []Pending) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range items {
		if _, err := tx.ExecContext(ctx, `UPDATE exchanges SET embedded_sha=? WHERE id=? AND sha=?`, p.Sha, p.ID, p.Sha); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// The origin of an exchange: whose window it was typed into. A subagent's exchange carries an agent id, the user's own
// session none, so no column says it. Reads default to main: what agents did must not fill the user's context.
const (
	OriginMain     = "main"
	OriginSubagent = "subagent"
	OriginAll      = "all"
)

// ValidOrigin says whether o names an origin to read; "" is main.
func ValidOrigin(o string) bool {
	return o == "" || o == OriginMain || o == OriginSubagent || o == OriginAll
}

// originCond is the condition on e.agent for a read of origin o, "" when it reads every origin.
func originCond(o string) string {
	switch o {
	case OriginAll:
		return ""
	case OriginSubagent:
		return `e.agent <> ''`
	}
	return `e.agent = ''`
}

// Filter narrows a search; Origin is main, subagent or all, empty for main; Archive also reads the archived rows.
type Filter struct {
	Archive bool
	Since   time.Time
	Repo    string
	Origin  string
	Key     string    // a project named by key or alias: it replaces Repo; see FindProject
	Names   []string  // the names of that project, for the exchanges stored before keys
	Until   time.Time // exclusive, so Since and Until tile
	Role    string    // "user" or "assistant" (from ParseRole): the full-text match is in the user's words or in the answer
}

// roleColumns maps a search role to the fts column it is matched in: the words are the user's, the answer is the
// assistant's text (not tool output). "" and "any" are no restriction.
var roleColumns = map[string]string{"user": "words", "assistant": "answer"}

// ParseRole reads a search role: "" and "any" are every role (returned as ""), "user" and "assistant" themselves.
func ParseRole(role string) (string, error) {
	if role == "" || role == "any" {
		return "", nil
	}
	if _, ok := roleColumns[role]; !ok {
		return "", fmt.Errorf("%q: user, assistant or any", role)
	}
	return role, nil
}

// ceilSecond rounds up to a whole second: a row is stored with whole seconds, so "before 00:00:00.5" is "before 00:00:01".
func ceilSecond(t time.Time) time.Time {
	if t.Nanosecond() == 0 {
		return t
	}
	return t.Truncate(time.Second).Add(time.Second)
}

// ftsMatch is the MATCH expression for the words of a query: each quoted, OR between them, in the role's column when
// there is one.
func ftsMatch(terms []string, role string) string {
	q := make([]string, len(terms))
	for i, t := range terms {
		q[i] = `"` + t + `"`
	}
	m := strings.Join(q, " OR ")
	if col := roleColumns[role]; col != "" {
		return "{" + col + "} : (" + m + ")"
	}
	return m
}

// RoleMatch keeps, of ids, those whose text for the role holds a word of the query: the same match FTS makes, so a
// candidate found by meaning passes only if it also says one of the words in that role's column. Without a role it
// keeps them all.
func (s *Store) RoleMatch(ctx context.Context, ids []int64, query, role string) ([]int64, error) {
	if roleColumns[role] == "" || len(ids) == 0 {
		return ids, nil
	}
	terms := Terms(query)
	if len(terms) == 0 {
		return nil, nil
	}
	args := []any{ftsMatch(terms, role)}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.r.QueryContext(ctx, `SELECT rowid FROM fts WHERE fts MATCH ? AND rowid IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ok := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ok[id] = true
	}
	var out []int64
	for _, id := range ids { // the order of ids is the caller's ranking
		if ok[id] {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

func (f Filter) where() (string, []any) {
	cond := []string{"1"}
	var args []any
	if !f.Since.IsZero() {
		cond = append(cond, `e.time >= ?`)
		args = append(args, stamp(f.Since))
	}
	if !f.Until.IsZero() {
		cond = append(cond, `e.time < ?`)
		args = append(args, stamp(ceilSecond(f.Until)))
	}
	if f.Key != "" {
		c, a, _ := namedBy(f.Key, f.Names)
		cond = append(cond, c)
		args = append(args, a...)
	} else if f.Repo != "" {
		cond = append(cond, `(e.cwd LIKE ? OR e.project LIKE ?)`)
		args = append(args, "%"+f.Repo+"%", "%"+f.Repo+"%")
	}
	if !f.Archive {
		cond = append(cond, `e.archived = 0`)
	}
	if c := originCond(f.Origin); c != "" {
		cond = append(cond, c)
	}
	return " AND " + strings.Join(cond, " AND "), args
}

// MaxTerms bounds the words of a query: the match grows faster than its input, and the first sixty-four say what is wanted.
const MaxTerms = 64

var termRE = regexp.MustCompile(`[\pL\pN_]+`)

// Terms splits a query into its words: each once, at most MaxTerms.
func Terms(q string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range termRE.FindAllString(q, -1) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
			if len(out) == MaxTerms {
				break
			}
		}
	}
	return out
}

// FTS returns ids by bm25: each word quoted, OR between them, because short questions rarely hold every word of the answer.
// The user's words count most, the tool output least.
func (s *Store) FTS(ctx context.Context, query string, n int, f Filter) ([]int64, error) {
	terms := Terms(query)
	if len(terms) == 0 {
		return nil, nil
	}
	where, args := f.where()
	q := `SELECT e.id FROM fts JOIN exchanges e ON e.id = fts.rowid WHERE fts MATCH ?` + where + ` ORDER BY bm25(fts, 5.0, 3.0, 1.0) LIMIT ?`
	rows, err := s.r.QueryContext(ctx, q, append(append([]any{ftsMatch(terms, f.Role)}, args...), n)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

const rowCols = `e.id, e.uuid, e.session, e.agent, e.seq, e.source, e.time, e.end_time, e.cwd, e.project, e.closed, e.cut_bytes, e.over_limit, e.sha, e.words, e.answer, e.archived`

func scanRow(sc interface{ Scan(...any) error }, extra ...any) (Row, error) {
	var r Row
	var t, end string
	dest := append([]any{&r.ID, &r.UUID, &r.Session, &r.Agent, &r.Seq, &r.Source, &t, &end, &r.Cwd, &r.Project, &r.Closed, &r.CutBytes, &r.OverLimit, &r.Sha, &r.Words, &r.Answer, &r.Archived}, extra...)
	if err := sc.Scan(dest...); err != nil {
		return r, err
	}
	r.Time, r.End = parse(t), parse(end)
	return r, nil
}

// Rows returns the listed exchanges that pass the filter, without the large texts unless asked for.
// withEmbed and withText are separate because each rerank form needs one of them and the full text can be megabytes.
func (s *Store) Rows(ctx context.Context, ids []int64, f Filter, withEmbed, withText bool) (map[int64]Row, error) {
	out := make(map[int64]Row, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+3)
	for _, id := range ids {
		args = append(args, id)
	}
	where, fargs := f.where()
	rows, err := s.r.QueryContext(ctx, `SELECT `+rowCols+`, `+colOr(withEmbed, "e.embed_text")+`, `+colOr(withText, "e.text")+` FROM exchanges e WHERE e.id IN (`+ph+`)`+where, append(args, fargs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var emb, text string
		r, err := scanRow(rows, &emb, &text)
		if err != nil {
			return nil, err
		}
		r.Embed, r.Text = emb, text
		out[r.ID] = r
	}
	return out, rows.Err()
}

func colOr(on bool, col string) string {
	if on {
		return col
	}
	return "''"
}

// Get returns one whole exchange.
func (s *Store) Get(ctx context.Context, id int64) (Row, error) {
	var text string
	r, err := scanRow(s.r.QueryRowContext(ctx, `SELECT `+rowCols+`, e.text FROM exchanges e WHERE e.id=?`, id), &text)
	r.Text = text
	return r, err
}

// ProjectCount is one project's share of the store.
type ProjectCount struct {
	Project  string `json:"project"`
	Chunks   int    `json:"chunks"`
	Earliest string `json:"earliest"`
	Latest   string `json:"latest"`
}

// Status is the state of the store.
type Status struct {
	Chunks     int            `json:"chunks"`
	Pending    int            `json:"pending_embeds"`
	Cut        int            `json:"cut_chunks"`
	OverLimit  int            `json:"over_limit_chunks"`
	Archived   int            `json:"archived"`
	Sessions   int            `json:"sessions"`
	Files      int            `json:"files"`
	LastIngest string         `json:"last_ingest"`
	Earliest   string         `json:"earliest"`
	Latest     string         `json:"latest"`
	Projects   []ProjectCount `json:"projects"`
}

// Stats reads the status. The large columns are not touched.
func (s *Store) Stats(ctx context.Context) (Status, error) {
	var st Status
	err := s.r.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(embedded_sha<>sha),0), coalesce(sum(cut_bytes>0),0), coalesce(sum(over_limit),0), coalesce(sum(archived),0),
		count(DISTINCT session), coalesce(min(time),''), coalesce(max(time),'') FROM exchanges`).
		Scan(&st.Chunks, &st.Pending, &st.Cut, &st.OverLimit, &st.Archived, &st.Sessions, &st.Earliest, &st.Latest)
	if err != nil {
		return st, err
	}
	if err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM cursors`).Scan(&st.Files); err != nil {
		return st, err
	}
	if err := s.r.QueryRowContext(ctx, `SELECT coalesce((SELECT value FROM meta WHERE key='last_ingest'),'')`).Scan(&st.LastIngest); err != nil {
		return st, err
	}
	rows, err := s.r.QueryContext(ctx, `SELECT project, count(*), min(time), max(time) FROM exchanges GROUP BY project ORDER BY count(*) DESC`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var p ProjectCount
		if err := rows.Scan(&p.Project, &p.Chunks, &p.Earliest, &p.Latest); err != nil {
			return st, err
		}
		st.Projects = append(st.Projects, p)
	}
	return st, rows.Err()
}
