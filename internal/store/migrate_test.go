// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// legacySchema is the schema of the version before projects had keys (commit 23ab403), verbatim: the fixture a migration
// test opens with the current code.
const legacySchema = `CREATE TABLE IF NOT EXISTS exchanges(
  id INTEGER PRIMARY KEY, uuid TEXT NOT NULL UNIQUE, session TEXT NOT NULL, agent TEXT NOT NULL, seq INTEGER NOT NULL, source TEXT NOT NULL,
  time TEXT NOT NULL, end_time TEXT NOT NULL, cwd TEXT NOT NULL, project TEXT NOT NULL, prio INTEGER NOT NULL, closed INTEGER NOT NULL,
  cut_bytes INTEGER NOT NULL, over_limit INTEGER NOT NULL, text_len INTEGER NOT NULL, sha TEXT NOT NULL, embedded_sha TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL, words TEXT NOT NULL, answer TEXT NOT NULL, embed_text TEXT NOT NULL, text TEXT NOT NULL);
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
CREATE TABLE IF NOT EXISTS kv(rid INTEGER PRIMARY KEY, key TEXT NOT NULL, value TEXT NOT NULL, time TEXT NOT NULL, deleted INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS kv_key ON kv(key, rid);
CREATE TABLE IF NOT EXISTS kv_vec(rid INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS kv_link(rid INTEGER PRIMARY KEY, s TEXT NOT NULL, p TEXT NOT NULL, o TEXT NOT NULL, time TEXT NOT NULL, deleted INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS kv_link_s ON kv_link(s, rid);
CREATE INDEX IF NOT EXISTS kv_link_o ON kv_link(o, rid);
CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY, time TEXT NOT NULL, event TEXT NOT NULL, session TEXT NOT NULL,
  cwd TEXT NOT NULL, tool TEXT NOT NULL, tool_use_id TEXT NOT NULL, duration_ms INTEGER, input TEXT NOT NULL, output TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS events_session ON events(session, event, id);
CREATE INDEX IF NOT EXISTS events_tool_use ON events(tool_use_id) WHERE tool_use_id <> '';
`

// legacyDB writes a file as the previous version left it: two exchanges of one project, a note and a link.
func legacyDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chatlog.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"u1", "u2"} {
		if _, err := db.Exec(`INSERT INTO exchanges(uuid, session, agent, seq, source, time, end_time, cwd, project, prio, closed, cut_bytes, over_limit, text_len, sha, updated_at, words, answer, embed_text, text)
			VALUES(?, 's', '', 0, 'f', '2026-11-01T10:00:00Z', '2026-11-01T10:00:00Z', '/u/webshop', 'webshop', 0, 1, 0, 0, 5, 'sha', '2026-11-01T10:00:00Z', 'w', 'a', 'e', 'needle ' || ?)`, u, u); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO kv(key, value, time, deleted) VALUES('rule/a', 'old rule', '2026-11-01T10:00:00.000000000Z', 0)`,
		`INSERT INTO kv(key, value, time, deleted) VALUES('now/b', 'old state', '2026-11-01T10:00:00.000000000Z', 0)`,
		`INSERT INTO kv_link(s, p, o, time, deleted) VALUES('rule/a', 'applies_to', 'sit/x', '2026-11-01T10:00:00.000000000Z', 0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestMigrationKeepsOldRowsAndShowsThemToEveryProject(t *testing.T) {
	path := legacyDB(t)
	ctx := context.Background()
	for pass := 1; pass <= 2; pass++ { // the second open must find nothing to do
		s, err := Open(path)
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		// the old notes are global: every project reads them, as before
		for _, p := range []string{"a-project", "another", GlobalProject} {
			got, err := s.KVRead(ctx, p, nil, []string{""}, time.Time{})
			if err != nil || len(got) != 2 || got[0].Project != GlobalProject {
				t.Fatalf("pass %d, project %s: %+v %v", pass, p, got, err)
			}
		}
		if links, _ := s.KVLinkedTo(ctx, "a-project", "sit/x", time.Time{}); len(links) != 1 || links[0] != "rule/a" {
			t.Fatalf("pass %d: the old link is global too: %v", pass, links)
		}
		// the old exchanges have no key: they are found by their name, and the index came through
		if items, _, err := s.Tail(ctx, "webshop", "", 0, 1000); err != nil || len(items) != 2 {
			t.Fatalf("pass %d: tail by the old name: %d %v", pass, len(items), err)
		}
		if ids, err := s.FTS(ctx, "needle", 10, Filter{}); err != nil || len(ids) != 2 {
			t.Fatalf("pass %d: %v %v", pass, ids, err)
		}
		// history is kept: a project that rewrites a key does not change the old row
		if pass == 1 {
			if _, err := s.KVWrite(ctx, "a-project", map[string]string{"rule/a": "new rule"}, nil); err != nil {
				t.Fatal(err)
			}
		}
		got, _ := s.KVRead(ctx, "a-project", []string{"rule/a"}, nil, time.Time{})
		other, _ := s.KVRead(ctx, "another", []string{"rule/a"}, nil, time.Time{})
		want := "new rule"
		if len(got) != 1 || got[0].Value != want || len(other) != 1 || other[0].Value != "old rule" {
			t.Fatalf("pass %d: %+v %+v", pass, got, other)
		}
		s.Close()
	}
}
