// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/atvirokodosprendimai/chatlog/internal/config"
	"github.com/atvirokodosprendimai/chatlog/internal/playbook"
	"github.com/atvirokodosprendimai/chatlog/internal/project"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
	"github.com/atvirokodosprendimai/chatlog/internal/wake"
)

func wakeCmd(cfg config.File) *cli.Command {
	return &cli.Command{
		Name:  "wake",
		Usage: "print the recovery digest the session-start hook injects: the rule, the notes, the latest exchanges with their tool calls",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "kind", Value: wake.Start, Usage: "compact (full), clear (notes only) or start"},
			&cli.StringFlag{Name: "project", Usage: "default: the base name of the current directory"},
			&cli.IntFlag{Name: "bytes", Value: cfg.Hooks.Wake(), Usage: "the cap"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if !slices.Contains(wake.Kinds, cmd.String("kind")) {
				return fmt.Errorf("--kind: one of %s", strings.Join(wake.Kinds, ", "))
			}
			p := cmd.String("project")
			if p == "" {
				cwd, _ := os.Getwd()
				p = project.Of(cwd)
			}
			st, err := openStore(cmd, 5*time.Second)
			if err != nil {
				return err
			}
			defer st.Close()
			d, err := wake.Digest(ctx, st, cmd.String("kind"), p, time.Now(), cmd.Int("bytes"))
			if err != nil {
				return err
			}
			fmt.Print(d)
			return nil
		},
	}
}

func seedCmd() *cli.Command {
	return &cli.Command{
		Name:  "seed",
		Usage: "write the starter notes (the playbook's rules under rule/ tool/ legend/, and one example each of hook/ and sit/) where a key is absent; safe to repeat",
		Flags: []cli.Flag{&cli.BoolFlag{Name: "dry-run", Usage: "list what would be written"}},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			st, err := openStore(cmd, 10*time.Second)
			if err != nil {
				return err
			}
			defer st.Close()
			missing, err := missingSeeds(ctx, st)
			if err != nil {
				return err
			}
			set := map[string]string{}
			for _, n := range missing {
				set[n.Key] = n.Value
				fmt.Printf("%s%s\n", map[bool]string{true: "would write ", false: "write "}[cmd.Bool("dry-run")], n.Key)
			}
			if len(set) == 0 {
				fmt.Printf("all %d starter notes are there\n", len(playbook.Seed))
				return nil
			}
			if cmd.Bool("dry-run") {
				return nil
			}
			_, err = st.KVWrite(ctx, set, nil)
			return err
		},
	}
}

// missingSeeds lists the starter notes whose key the store does not hold; a key the user rewrote counts as there.
func missingSeeds(ctx context.Context, st *store.Store) ([]playbook.Note, error) {
	keys := make([]string, len(playbook.Seed))
	for i, n := range playbook.Seed {
		keys[i] = n.Key
	}
	have, err := st.KVHas(ctx, keys)
	if err != nil {
		return nil, err
	}
	var out []playbook.Note
	for _, n := range playbook.Seed {
		if !have[n.Key] {
			out = append(out, n)
		}
	}
	return out, nil
}
