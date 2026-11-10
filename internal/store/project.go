// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/atvirokodosprendimai/chatlog/internal/project"
)

// A project is named by a key (package project: the root commit's sha, "path:<root>" without a repository, or a literal
// name set with CHATLOG_PROJECT). People use aliases: names that each stand for one key. The folder's base name becomes an
// alias when an exchange of that project is first stored; `chatlog project alias` adds and removes the others. Wherever a
// project is named, an alias, a short sha ([526abcd] or 526abcd) or a key is accepted.

var (
	aliasRE = regexp.MustCompile(`^[^\s\[\]*/:]{1,64}$`)
	hexRE   = regexp.MustCompile(`^[0-9a-f]{4,64}$`)
)

func shortKey(key string) string { return project.Short(key) }

// validAlias refuses a name that could be taken for something else: a short sha, a key, a scope.
func validAlias(name string) error {
	if !aliasRE.MatchString(name) || hexRE.MatchString(name) || name == GlobalProject || strings.HasPrefix(name, "path:") {
		return refuse("alias %q: 1 to 64 characters, no spaces, brackets, / : or *, not global, and not a string of hex digits (a short sha)", name)
	}
	return nil
}

// RootKey and PutRootKey keep the root commit of a repository by its root path (see project.Cache).
func (s *Store) RootKey(root string) (string, bool) {
	var key string
	err := s.r.QueryRow(`SELECT key FROM project_root WHERE root = ?`, root).Scan(&key)
	return key, err == nil
}

// PutRootKey records the root commit of the repository at root.
func (s *Store) PutRootKey(root, key string) {
	s.w.Exec(`INSERT INTO project_root(root, key) VALUES(?, ?) ON CONFLICT(root) DO UPDATE SET key = excluded.key`, root, key)
}

// ResolveProject turns a project as a caller named it into its key: an alias, a short sha, a key, "global" or "*" (kept
// as they are). A name that is none of these is a project of its own, under that name (a fleet agent's CHATLOG_PROJECT).
func (s *Store) ResolveProject(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	if n, ok := strings.CutPrefix(name, "["); ok {
		name = strings.TrimSuffix(n, "]")
	}
	if l := strings.ToLower(name); l != name && hexRE.MatchString(l) {
		name = l // a short sha typed in capitals
	}
	switch name {
	case "":
		return "", refuse("a project name is empty: the MCP server sends its own, so restart a session that began before projects existed")
	case AllProjects, GlobalProject:
		return name, nil
	}
	var key string
	switch err := s.r.QueryRowContext(ctx, `SELECT key FROM project_alias WHERE alias = ?`, name).Scan(&key); {
	case err == nil:
		return key, nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", err
	}
	if project.IsSha(name) || strings.HasPrefix(name, "path:") || !hexRE.MatchString(name) {
		return name, nil
	}
	rows, err := s.r.QueryContext(ctx, `SELECT key FROM project_alias WHERE key LIKE ?1 UNION SELECT project FROM kv WHERE project LIKE ?1
		UNION SELECT pkey FROM exchanges WHERE pkey LIKE ?1`, name+"%")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return "", err
		}
		if project.IsSha(k) {
			keys = append(keys, k)
		}
	}
	switch {
	case rows.Err() != nil:
		return "", rows.Err()
	case len(keys) == 1:
		return keys[0], nil
	case len(keys) > 1:
		return "", refuse("%s is the start of %d projects: give more of the sha, or an alias", name, len(keys))
	}
	return name, nil
}

// Aliases lists a key's aliases, the first given first.
func (s *Store) Aliases(ctx context.Context, key string) ([]string, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT alias FROM project_alias WHERE key = ? ORDER BY rowid`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// FindProject resolves a name as ResolveProject does and adds the names the project's rows may carry: its aliases and the
// name as given, since an exchange stored before projects had keys has a name and no key. found is false when the name is
// no alias and no key: a plain word, which a search then reads as part of a path.
func (s *Store) FindProject(ctx context.Context, name string) (key string, names []string, found bool, err error) {
	if key, err = s.ResolveProject(ctx, name); err != nil {
		return "", nil, false, err
	}
	if names, err = s.Aliases(ctx, key); err != nil {
		return "", nil, false, err
	}
	found = len(names) > 0 || project.IsSha(key) || strings.HasPrefix(key, "path:")
	if n := strings.TrimSpace(name); !slices.Contains(names, n) {
		names = append(names, n)
	}
	return key, names, found, nil
}

// projectCond is the condition on an exchange for a project as named: the rows of its key, and the older rows that have no
// key and carry one of its names.
func (s *Store) projectCond(ctx context.Context, name string) (string, []any, error) {
	key, names, _, err := s.FindProject(ctx, name)
	if err != nil {
		return "", nil, err
	}
	return namedBy(key, names)
}

func namedBy(key string, names []string) (string, []any, error) {
	args := []any{key}
	for _, n := range names {
		args = append(args, n)
	}
	return `(e.pkey = ? OR (e.pkey = '' AND e.project IN (` + strings.TrimSuffix(strings.Repeat("?,", len(names)), ",") + `)))`, args, nil
}

// Label is how a project is shown: its first alias, else its short key.
func (s *Store) Label(ctx context.Context, name string) string {
	key, err := s.ResolveProject(ctx, name)
	if err != nil {
		return name
	}
	if names, _ := s.Aliases(ctx, key); len(names) > 0 {
		return names[0]
	}
	return shortKey(key)
}

// AddAlias gives a project a name. An alias stands for one key: a name another key holds is refused; the same key twice is not.
func (s *Store) AddAlias(ctx context.Context, name, alias string) error {
	if err := validAlias(alias); err != nil {
		return err
	}
	key, err := s.ResolveProject(ctx, name)
	if err != nil {
		return err
	}
	if key == AllProjects || key == GlobalProject || hexRE.MatchString(key) && !project.IsSha(key) {
		return refuse("%q names no project: give an alias, the whole sha or a short one that exists (chatlog project ls)", name)
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var have string
	switch err := tx.QueryRowContext(ctx, `SELECT key FROM project_alias WHERE alias = ?`, alias).Scan(&have); {
	case err == nil && have != key:
		return refuse("alias %q names %s already: remove it first (chatlog project alias -d %s)", alias, shortKey(have), alias)
	case err == nil:
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO project_alias(alias, key) VALUES(?, ?)`, alias, key); err != nil {
		return err
	}
	return tx.Commit()
}

// DelAlias removes an alias; the project, its rows and its other names stay.
func (s *Store) DelAlias(ctx context.Context, alias string) error {
	res, err := s.w.ExecContext(ctx, `DELETE FROM project_alias WHERE alias = ?`, alias)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return refuse("no alias %q", alias)
	}
	return nil
}

// ProjectInfo is a project as `chatlog project ls` lists it.
type ProjectInfo struct {
	Key       string   `json:"key"`
	Aliases   []string `json:"aliases"`
	Exchanges int      `json:"exchanges"`
	Notes     int      `json:"notes"`
}

// Projects lists every key the store knows: from aliases, exchanges and notes.
func (s *Store) Projects(ctx context.Context) ([]ProjectInfo, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT key FROM project_alias UNION SELECT pkey FROM exchanges WHERE pkey <> '' UNION SELECT project FROM kv WHERE project <> '' ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	var out []ProjectInfo
	for _, k := range keys {
		p := ProjectInfo{Key: k}
		if p.Aliases, err = s.Aliases(ctx, k); err != nil {
			return nil, err
		}
		if err := s.r.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM exchanges WHERE pkey = ?1), (SELECT count(DISTINCT key) FROM kv WHERE project = ?1)`, k).Scan(&p.Exchanges, &p.Notes); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// String is a project as one line: [526abcd] alias alias (N exchanges, N notes).
func (p ProjectInfo) String() string {
	return fmt.Sprintf("%s  %s  (%d exchanges, %d notes)", shortKey(p.Key), strings.Join(p.Aliases, " "), p.Exchanges, p.Notes)
}
