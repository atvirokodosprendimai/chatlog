// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/atvirokodosprendimai/chatlog/internal/aam"
	"github.com/atvirokodosprendimai/chatlog/internal/chunk"
	"github.com/atvirokodosprendimai/chatlog/internal/ingest"
	"github.com/atvirokodosprendimai/chatlog/internal/redact"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
	"github.com/atvirokodosprendimai/chatlog/internal/transcript"
)

// importBatch is how many documents one transaction holds.
const importBatch = 200

func importAAMCmd() *cli.Command {
	return &cli.Command{
		Name: "import-aam", ArgsUsage: "FILE",
		Usage: "read another memory's SQLite file (its drawers, with their facts) into the history as archived rows: kept, hidden from every read until chat_archive restores one; safe to repeat",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "team", Usage: "the team whose drawers to read; default the only one"},
			&cli.StringSliceFlag{Name: "wing", Usage: "only this wing, with or without the wing_ prefix (repeatable); default all"},
			&cli.StringSliceFlag{Name: "project-of", Usage: "wing=project: the project a wing's rows belong to (repeatable); default the wing's name without wing_; the facts no drawer holds are the wing facts"},
			&cli.BoolFlag{Name: "dry-run", Usage: "count what would be imported, write nothing"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() != 1 {
				return fmt.Errorf("usage: chatlog import-aam FILE [--team T] [--wing W]... [--project-of wing=project]... [--dry-run]")
			}
			mapping := map[string]string{}
			for _, kv := range cmd.StringSlice("project-of") {
				w, p, ok := strings.Cut(kv, "=")
				if !ok || strings.TrimSpace(w) == "" || strings.TrimSpace(p) == "" {
					return fmt.Errorf("--project-of %q: want wing=project", kv)
				}
				mapping[strings.TrimPrefix(strings.TrimSpace(w), "wing_")] = strings.TrimSpace(p)
			}
			return importAAM(ctx, cmd, cmd.Args().First(), cmd.String("team"), cmd.StringSlice("wing"), mapping, cmd.Bool("dry-run"))
		},
	}
}

func importAAM(ctx context.Context, cmd *cli.Command, file, team string, wings []string, mapping map[string]string, dry bool) error {
	r, err := aam.Open(file)
	if err != nil {
		return err
	}
	defer r.Close()
	teams, err := r.Teams(ctx)
	if err != nil {
		return err
	}
	if team, err = aam.PickTeam(teams, team); err != nil {
		return err
	}
	abs, _ := filepath.Abs(file)
	source := "aam:" + filepath.Base(file)
	var st *store.Store
	var in *ingest.Ingester
	var before store.Status
	if !dry {
		if st, err = openStore(cmd, 10*time.Second); err != nil {
			return err
		}
		defer st.Close()
		in = &ingest.Ingester{Store: st, Limit: chunk.DefaultLimit}
		if before, err = st.Stats(ctx); err != nil {
			return err
		}
	}
	var rows []store.Row
	done := 0
	flush := func() error {
		if len(rows) == 0 || dry {
			rows = nil
			return nil
		}
		err := st.Apply(ctx, "aam:"+abs+":"+team, rows, store.Cursor{Offset: int64(done), ReadTo: int64(done)})
		rows = nil
		return err
	}
	projects := map[string]struct{ name, key string }{} // per wing, resolved once
	start := time.Now()
	stats, err := r.Read(ctx, aam.Options{Team: team, Wings: wings}, func(d aam.Doc) error {
		done++
		if dry {
			return nil
		}
		p, ok := projects[d.Wing]
		if !ok {
			p.name = mapping[strings.TrimPrefix(d.Wing, "wing_")]
			if p.name == "" {
				p.name = strings.TrimPrefix(d.Wing, "wing_")
			}
			if key, _, found, err := st.FindProject(ctx, p.name); err == nil && found { // the project's rows carry its key when it has one
				p.key = key
			}
			projects[d.Wing] = p
		}
		words := d.Wing + "/" + d.Room
		if d.Topic != "" {
			words += " - " + d.Topic
		}
		row := in.ToRow(transcript.Exchange{UUID: "aam:" + d.ID, Session: "aam:" + d.Wing, Source: source, Time: d.Filed, End: d.Filed,
			Words: words, Answer: redact.String(d.Text), Closed: true})
		row.Archived, row.Project, row.PKey, row.Refs = true, p.name, p.key, d.Refs
		rows = append(rows, row)
		if len(rows) >= importBatch {
			return flush()
		}
		return nil
	})
	if err == nil {
		err = flush()
	}
	if err != nil {
		return err
	}
	verb := "imported"
	if dry {
		verb = "would import"
	}
	fmt.Printf("%s %d documents of team %s in %v: %d drawers (%d of them several chunks joined, %d ended), %d facts appended to their drawers, and %d facts in %d documents of their own (the file names no drawer for them)\n",
		verb, stats.Drawers+stats.FactDocs, team, time.Since(start).Round(time.Millisecond), stats.Drawers, stats.Joined, stats.Ended, stats.Facts, stats.LooseFacts, stats.FactDocs)
	if stats.SkippedFacts > 0 {
		fmt.Printf("%d facts belong to drawers of wings that were not asked for: left out\n", stats.SkippedFacts)
	}
	if dry {
		return nil
	}
	after, err := st.Stats(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%d rows new, %d already there; all archived (chatlog search --archive finds them; chat_archive restores one); %d rows wait for a vector: the server makes them with its embedder, in the queue every row uses\n",
		after.Chunks-before.Chunks, done-(after.Chunks-before.Chunks), after.Pending)
	return nil
}
