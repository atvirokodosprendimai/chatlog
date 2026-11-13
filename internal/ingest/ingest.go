// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package ingest moves transcripts into the stores: files -> exchanges -> SQLite, then pending texts -> TEI -> vectors.
// The two run apart, so TEI being down costs only vectors, and no HTTP call happens inside a transaction.
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/chunk"
	"github.com/atvirokodosprendimai/chatlog/internal/project"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
	"github.com/atvirokodosprendimai/chatlog/internal/tei"
	"github.com/atvirokodosprendimai/chatlog/internal/transcript"
	"github.com/atvirokodosprendimai/chatlog/internal/vector"
)

const (
	flushBytes   = 32 << 20        // commit before holding more text than this in memory
	embedBatch   = 1               // ~3.7k tokens each at 2-4k tokens/s: a search query queued behind them still fits its 5 s
	embedWorkers = 1               // more workers only lengthen that queue: the GPU is the limit, not the client
	quietAfter   = 5 * time.Minute // an open exchange gets its vector once it stopped growing
)

// Ingester holds what a scan and an embed round need.
type Ingester struct {
	Store   *store.Store
	Vec     *vector.Index
	KVVec   *vector.Index // the notes' vectors; nil leaves the notes unembedded
	TEI     *tei.Client
	Sources []string // directories of projects, or files, or globs
	Since   time.Time
	Limit   int
	// MaxBehind, when above 0, makes File skip a file with more than this many bytes to read from its cursor and return
	// ErrBehind: a hook reads only what one turn added, and leaves a backlog to the server or `chatlog ingest`.
	MaxBehind int64
}

// ErrBehind says a file was left for a later, unbounded read: see Ingester.MaxBehind.
var ErrBehind = errors.New("more to read than this caller reads")

// Files lists the transcripts of the sources, newest first: search is usable early.
func (in *Ingester) Files() ([]string, error) {
	type file struct {
		path string
		mod  time.Time
	}
	var files []file
	seen := map[string]bool{}
	add := func(p string) {
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() || seen[p] || fi.ModTime().Before(in.Since) {
			return
		}
		seen[p] = true
		files = append(files, file{p, fi.ModTime()})
	}
	for _, src := range in.Sources {
		var patterns []string
		if fi, err := os.Stat(src); err == nil && fi.IsDir() {
			patterns = []string{filepath.Join(src, "*", "*.jsonl"), filepath.Join(src, "*", "*", "subagents", "*.jsonl")}
		} else {
			patterns = []string{src}
		}
		for _, pat := range patterns {
			matches, err := filepath.Glob(pat)
			if err != nil {
				return nil, err
			}
			for _, m := range matches {
				add(m)
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.path
	}
	return out, nil
}

// Scan reads what every file gained.
func (in *Ingester) Scan(ctx context.Context) error {
	files, err := in.Files()
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := in.File(ctx, f); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
	}
	return nil
}

// ToRow is the pure part: redaction already done by the parser, cut here, outside any transaction.
func (in *Ingester) ToRow(e transcript.Exchange) store.Row {
	c := chunk.Build(e, in.Limit)
	sum := sha256.Sum256([]byte(c.Full))
	id := project.FromCwd(e.Cwd, in.Store) // never the override: this may be a server a fleet agent's call started
	return store.Row{
		UUID: e.UUID, Session: e.Session, Agent: e.Agent, Seq: e.Offset, Source: e.Source, Time: e.Time, End: e.End, Cwd: e.Cwd,
		Project: id.Name, PKey: id.Key, Closed: e.Closed, CutBytes: c.CutBytes, OverLimit: c.OverLimit,
		Sha: hex.EncodeToString(sum[:8]), Words: e.Words, Answer: e.Answer, Embed: c.Embed, Text: c.Full,
	}
}

// File reads one file from its cursor. Rows and the cursor commit together, in groups, so a kill mid-file costs a re-read of
// the last group's text at most, never a lost or doubled exchange.
func (in *Ingester) File(ctx context.Context, path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	cur, err := in.Store.CursorOf(ctx, path)
	if err != nil {
		return err
	}
	if fi.Size() == cur.ReadTo && cur.ReadTo > 0 {
		return nil
	}
	if fi.Size() < cur.ReadTo { // rewritten: its rows come again under the same uuids
		cur = store.Cursor{}
	}
	if in.MaxBehind > 0 && fi.Size()-cur.Offset > in.MaxBehind {
		return ErrBehind
	}
	var rows []store.Row
	held := 0
	keep := func(e transcript.Exchange) {
		if in.Since.IsZero() || e.Time.IsZero() || !e.Time.Before(in.Since) {
			r := in.ToRow(e)
			rows = append(rows, r)
			held += len(r.Text)
		}
	}
	tail, err := transcript.Read(path, cur.Offset, func(e transcript.Exchange, next int64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		keep(e)
		if held < flushBytes {
			return nil
		}
		err := in.Store.Apply(ctx, path, rows, store.Cursor{Offset: next, ReadTo: next})
		rows, held = nil, 0
		return err
	})
	if err != nil {
		return err
	}
	if tail.Open != nil {
		keep(*tail.Open)
	}
	return in.Store.Apply(ctx, path, rows, store.Cursor{Offset: tail.Cursor, ReadTo: tail.ReadTo})
}

// Embed makes vectors until nothing is pending or TEI fails; the failure is returned and the rest stays pending for the next round.
// drain also takes exchanges still open, for a run that will not come back. Without an embedder it does nothing.
func (in *Ingester) Embed(ctx context.Context, drain bool) error {
	if in.TEI == nil || in.TEI.EmbedURL == "" {
		return nil
	}
	if err := in.embedKV(ctx); err != nil {
		return fmt.Errorf("kv: %w", err)
	}
	for {
		var quiet time.Time
		if !drain {
			quiet = time.Now().Add(-quietAfter)
		}
		items, err := in.Store.PendingEmbeds(ctx, embedBatch*embedWorkers, quiet)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		var wg sync.WaitGroup
		errs := make([]error, embedWorkers)
		for w := range embedWorkers {
			lo := w * embedBatch
			if lo >= len(items) {
				break
			}
			batch := items[lo:min(lo+embedBatch, len(items))]
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[w] = in.embedBatch(ctx, batch)
			}()
		}
		wg.Wait()
		for _, e := range errs {
			if e != nil {
				return e
			}
		}
	}
}

func (in *Ingester) embedBatch(ctx context.Context, batch []store.Pending) error {
	texts := make([]string, len(batch))
	for i, p := range batch {
		texts[i] = p.Text
	}
	vecs, err := in.TEI.Embed(ctx, texts)
	if err != nil {
		return err
	}
	for i, p := range batch {
		if err := in.Vec.Add(ctx, p.ID, vecs[i]); err != nil {
			return err
		}
	}
	return in.Store.MarkEmbedded(ctx, batch)
}

// embedKV gives every note waiting for one a vector. Notes are few and short, so one batch at a time, in order.
func (in *Ingester) embedKV(ctx context.Context) error {
	if in.KVVec == nil {
		return nil
	}
	for {
		items, err := in.Store.KVPending(ctx, embedBatch)
		if err != nil || len(items) == 0 {
			return err
		}
		texts := make([]string, len(items))
		for i, p := range items {
			texts[i] = p.Text
		}
		vecs, err := in.TEI.Embed(ctx, texts)
		if err != nil {
			return err
		}
		for i, p := range items {
			if err := in.KVVec.Add(ctx, p.ID, vecs[i]); err != nil {
				return err
			}
		}
		if err := in.Store.KVMarkEmbedded(ctx, items); err != nil {
			return err
		}
	}
}

// Run scans and embeds on the interval until ctx ends. The two loops are apart so a slow embed does not delay new text.
func (in *Ingester) Run(ctx context.Context, every time.Duration) {
	var wg sync.WaitGroup
	loop := func(name string, f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				start := time.Now()
				if err := f(); err != nil && ctx.Err() == nil {
					log.Printf("%s: %v (retried in %v)", name, err, every)
				} else if d := time.Since(start); d > 5*time.Second {
					log.Printf("%s: round took %v", name, d.Round(time.Second))
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(every):
				}
			}
		}()
	}
	loop("ingest", func() error { return in.Scan(ctx) })
	loop("embed", func() error { return in.Embed(ctx, false) })
	wg.Wait()
}
