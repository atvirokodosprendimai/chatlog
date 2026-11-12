// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package project says which project a session or a call belongs to, in one place for everything that reads by project:
// the tail, the wake digest, the notes.
//
// A project is the repository the work is in, named by its root commit: that survives a renamed or moved folder and a
// changed remote. A directory outside any repository is named by its path. The names people use are aliases the store keeps
// (see store.ResolveProject); the folder's base name is the first one. The CHATLOG_PROJECT variable overrides all of it with
// a name, a short sha or an alias: an agent working in ~/work/x for the project y says so there.
//
// A directory is named by its path with the links resolved (EvalSymlinks): the same directory reached through a link, or
// spelled by Claude Code one way and by the system's working directory another (macOS: /var is /private/var), is one
// project. A repository's key is its root commit and never depended on the spelling. Notes already stored under a
// "path:<link spelling>" key are not moved: they stay readable by naming that key (project="path:/the/old/spelling"), and
// `chatlog project ls` lists it; only a non-repository directory reached through a link is affected.
// Two clones of one repository share a root commit and so are one project; to keep them apart, set CHATLOG_PROJECT in one.
package project

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ID is a project as a call names it.
type ID struct {
	Key  string // the root commit's sha; "path:<root>" without a repository; the CHATLOG_PROJECT value as given
	Name string // the base name of the root: the alias first seen
	Root string // the repository root, or the cwd without a repository; "" for an override
	Env  bool   // Key is the CHATLOG_PROJECT value: it may be an alias or a short sha, for the store to resolve
}

// Cache remembers the root commit of a repository by its root path, so a hook does not walk a repository's history at
// every call: for a large one that costs seconds. A nil Cache is no cache.
type Cache interface {
	RootKey(root string) (key string, ok bool)
	PutRootKey(root, key string)
}

var shaRE = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// gitWait is the longest Resolve waits for git; past it the project is named by its path, and is not cached.
const gitWait = 2 * time.Second

var memo sync.Map   // root -> key, for the life of the process
var failed sync.Map // root -> when git last failed to name its root commit

// Resolve names the project of a call: the override, else the project of the cwd.
func Resolve(cwd string, c Cache) ID {
	if p := strings.TrimSpace(os.Getenv("CHATLOG_PROJECT")); p != "" {
		return ID{Key: p, Name: p, Env: true}
	}
	return FromCwd(cwd, c)
}

// FromCwd names the project of a directory, whatever the environment says: the repository root's commit, else the root
// path. What is read out of a transcript is named this way: the server that ingests it may have been started by a call
// that has an override of its own.
func FromCwd(cwd string, c Cache) ID {
	if cwd == "" {
		return ID{Key: "unknown", Name: "unknown"}
	}
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real // one directory is one project however it is spelled: the hook is given Claude Code's spelling, the CLI the system's
	}
	root := Root(cwd)
	id := ID{Key: "path:" + root, Name: filepath.Base(root), Root: root}
	if v, ok := memo.Load(root); ok {
		id.Key = v.(string)
		return id
	}
	if c != nil {
		if k, ok := c.RootKey(root); ok {
			memo.Store(root, k)
			id.Key = k
			return id
		}
	}
	if t, ok := failed.Load(root); ok && time.Since(t.(time.Time)) < time.Minute {
		return id // git did not answer a moment ago: not again at every call of this process
	}
	if k, err := RootCommit(root); err == nil {
		memo.Store(root, k)
		if c != nil {
			c.PutRootKey(root, k)
		}
		id.Key = k
	} else {
		failed.Store(root, time.Now())
	}
	return id
}

// Root is the nearest directory at or above cwd that holds a .git (a directory, or a file in a worktree), else cwd.
func Root(cwd string) string {
	cwd = filepath.Clean(cwd)
	for dir := cwd; ; {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return cwd
		}
		dir = parent
	}
}

// RootCommit is the sha of the oldest root commit of the repository at root (several when histories were merged).
func RootCommit(root string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitWait)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-list", "--max-parents=0", "--reverse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if !shaRE.MatchString(first) {
		return "", os.ErrNotExist
	}
	return first, nil
}

// Short is a key as people read it: [526abcd] for a sha, the key itself otherwise.
func Short(key string) string {
	if shaRE.MatchString(key) {
		return "[" + key[:7] + "]"
	}
	return key
}

// IsSha says whether s is a whole root commit sha.
func IsSha(s string) bool { return shaRE.MatchString(s) }
