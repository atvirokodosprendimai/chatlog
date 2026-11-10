// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/atvirokodosprendimai/chatlog/internal/api"
	"github.com/atvirokodosprendimai/chatlog/internal/chunk"
	"github.com/atvirokodosprendimai/chatlog/internal/config"
	"github.com/atvirokodosprendimai/chatlog/internal/hook"
	"github.com/atvirokodosprendimai/chatlog/internal/ingest"
	"github.com/atvirokodosprendimai/chatlog/internal/search"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

const (
	hookBudget = 3 * time.Second // the whole hook; Claude Code's own timeout for it is set to 10 s by install
	hookBusy   = time.Second     // the longest a hook waits for the server's write lock
)

func openStore(cmd *cli.Command, busy time.Duration) (*store.Store, error) {
	return store.OpenWith(filepath.Join(dataDir(cmd), "chatlog.db"), busy)
}

func hookCmd(cfg config.File, cfgErr error) *cli.Command {
	return &cli.Command{
		Name: "hook", ArgsUsage: "EVENT",
		Usage: "handle one Claude Code hook event, its JSON on stdin; always exits 0, failures go to hooks.log in the data dir",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			dir := dataDir(cmd)
			defer func() {
				if r := recover(); r != nil {
					hook.Log(dir, time.Now(), "hook %s: panic: %v", cmd.Args().First(), r)
				}
			}()
			if cfgErr != nil {
				hook.Log(dir, time.Now(), "config ignored, defaults used: %v", cfgErr)
			}
			ctx, cancel := context.WithTimeout(ctx, hookBudget)
			defer cancel()
			st, err := openStore(cmd, hookBusy)
			if err != nil {
				hook.Log(dir, time.Now(), "hook %s: %v", cmd.Args().First(), err)
				return nil
			}
			defer st.Close()
			spool := filepath.Join(dir, "spool.jsonl")
			// the notes' search by meaning needs the server's embedder; a server that is not running is not started for a hook
			client := api.NewClient(socketPath(cmd))
			recall := func(ctx context.Context, project, q string, limit int) ([]search.KVHit, error) {
				return client.KVSearch(ctx, api.KVSearchRequest{Project: project, Query: q, Limit: limit})
			}
			if err := hook.Run(ctx, cmd.Args().First(), os.Stdin, os.Stdout, hook.Deps{Store: st, Config: cfg, Now: time.Now, Spool: spool, Recall: recall}); err != nil {
				hook.Log(dir, time.Now(), "hook %s: %v", cmd.Args().First(), err)
			}
			return nil
		},
	}
}

func ingestCmd(cfg config.File) *cli.Command {
	return &cli.Command{
		Name: "ingest", ArgsUsage: "[FILE...]",
		Usage: "read transcripts into the history once, from where each was left: --all for every source; safe to repeat, no socket needed",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "all", Usage: "every transcript of the sources"},
			sourceFlag(cfg),
			&cli.StringFlag{Name: "since", Value: cfg.Since, Usage: "exchanges older than this are not indexed (YYYY-MM-DD); empty: all"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			files := cmd.Args().Slice()
			if cmd.Bool("all") == (len(files) > 0) {
				return fmt.Errorf("give --all or files, one of the two")
			}
			since, err := search.ParseSince(cmd.String("since"))
			if err != nil {
				return err
			}
			st, err := openStore(cmd, 10*time.Second)
			if err != nil {
				return err
			}
			defer st.Close()
			in := &ingest.Ingester{Store: st, Sources: cmd.StringSlice("source"), Since: since, Limit: chunk.DefaultLimit}
			if cmd.Bool("all") {
				if files, err = in.Files(); err != nil {
					return err
				}
			}
			before, err := st.Stats(ctx)
			if err != nil {
				return err
			}
			start := time.Now()
			for _, f := range files {
				if err := in.File(ctx, f); err != nil {
					return fmt.Errorf("%s: %w", f, err)
				}
			}
			s, err := st.Stats(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("%d files read in %v: %d new exchanges; the history holds %d exchanges of %d sessions\n",
				len(files), time.Since(start).Round(time.Millisecond), s.Chunks-before.Chunks, s.Chunks, s.Sessions)
			return nil
		},
	}
}

func eventsCmd() *cli.Command {
	return &cli.Command{
		Name: "events", Usage: "the latest hook records (prompts, tool calls, session boundaries) as JSON lines, oldest first; no socket needed",
		Flags: []cli.Flag{&cli.IntFlag{Name: "limit", Value: 20}, &cli.StringFlag{Name: "session", Usage: "only this session's"}},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			st, err := openStore(cmd, 10*time.Second)
			if err != nil {
				return err
			}
			defer st.Close()
			evs, err := st.Events(ctx, cmd.String("session"), cmd.Int("limit"))
			if err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			for _, e := range evs {
				if err := enc.Encode(e); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
