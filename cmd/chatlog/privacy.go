// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/atvirokodosprendimai/chatlog/internal/search"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
	"github.com/atvirokodosprendimai/chatlog/internal/vector"
)

func forgetCmd() *cli.Command {
	return &cli.Command{
		Name:  "forget",
		Usage: "delete one session, or everything before a date, from the history: exchanges, hook records and vectors (stop the server first)",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "session", Usage: "the session id (chat_search and chatlog events show it)"},
			&cli.StringFlag{Name: "before", Usage: "YYYY-MM-DD or RFC3339: everything older"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			session, before := cmd.String("session"), cmd.String("before")
			if (session == "") == (before == "") {
				return errors.New("give --session or --before, one of the two")
			}
			t, err := search.ParseSince(before)
			if err != nil {
				return err
			}
			// the server holds the vectors in memory and writes them back: deleting under it would not last
			if c, err := net.DialTimeout("unix", socketPath(cmd), time.Second); err == nil {
				c.Close()
				return errors.New("the chatlog server is running: stop it (and Claude Code sessions, whose chatlog mcp restarts it, or set CHATLOG_AUTOSTART=0), then run forget again")
			}
			st, err := openStore(cmd, 10*time.Second)
			if err != nil {
				return err
			}
			defer st.Close()
			ids, evs, err := st.Forget(ctx, session, t)
			if err != nil {
				return err
			}
			vec, err := vector.Open(filepath.Join(dataDir(cmd), "vectors"))
			if err != nil {
				return err
			}
			if err := vec.Delete(ctx, ids...); err != nil {
				return err
			}
			fmt.Printf("forgot %d exchanges and %d hook records; the transcripts in Claude Code's own directory are untouched: delete those there if they must go too\n", len(ids), evs)
			return nil
		},
	}
}

func exportCmd() *cli.Command {
	return &cli.Command{
		Name:  "export",
		Usage: "every exchange with its whole text as JSON lines on stdout, oldest first; no socket needed",
		Flags: []cli.Flag{&cli.StringFlag{Name: "session", Usage: "only this session's"}},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			st, err := openStore(cmd, 10*time.Second)
			if err != nil {
				return err
			}
			defer st.Close()
			enc := json.NewEncoder(os.Stdout)
			return st.Export(ctx, cmd.String("session"), func(r store.Row) error {
				return enc.Encode(struct {
					store.Row
					Text string `json:"text"`
				}{r, r.Text})
			})
		},
	}
}
