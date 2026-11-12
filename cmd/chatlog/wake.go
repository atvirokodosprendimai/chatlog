// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/atvirokodosprendimai/chatlog/internal/config"
	"github.com/atvirokodosprendimai/chatlog/internal/know"
	"github.com/atvirokodosprendimai/chatlog/internal/playbook"
	"github.com/atvirokodosprendimai/chatlog/internal/project"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
	"github.com/atvirokodosprendimai/chatlog/internal/wake"
)

func wakeCmd(cfg config.File) *cli.Command {
	return &cli.Command{
		Name:  "wake",
		Usage: "load what the wake list names (files, kv prefixes, steps), whole; with no list: the recovery digest the session-start hook injects",
		Description: "With a wake list in the global notes (wake/file, wake/prefix, wake/step; `chatlog wake load FILE` writes it) this prints what to read\n" +
			"whole and the LOADED line to answer with; --dump DIR writes it all into pages of at most 24000 bytes to read in one parallel turn.\n" +
			"Without a list, or with --digest, --kind or --bytes, it prints the digest: the rule, the notes, the latest exchanges.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "kind", Value: wake.Start, Usage: "digest: compact (full), clear (notes only) or start"},
			&cli.StringFlag{Name: "project", Usage: "default: the project of the current directory"},
			&cli.IntFlag{Name: "bytes", Value: cfg.Hooks.Wake(), Usage: "digest: the cap"},
			&cli.BoolFlag{Name: "digest", Usage: "print the digest even when a wake list exists"},
			&cli.StringFlag{Name: "dump", Usage: "write the list's content into DIR as page-NN.txt (at most 24000 bytes each) and print the pages and the LOADED counts"},
		},
		Commands: []*cli.Command{wakeLoadCmd()},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if !slices.Contains(wake.Kinds, cmd.String("kind")) {
				return fmt.Errorf("--kind: one of %s", strings.Join(wake.Kinds, ", "))
			}
			st, err := openStore(cmd, 5*time.Second)
			if err != nil {
				return err
			}
			defer st.Close()
			p := cmd.String("project")
			cwd, _ := os.Getwd()
			if p == "" {
				p = project.Resolve(cwd, st).Key
			}
			if !cmd.Bool("digest") && !cmd.IsSet("kind") && !cmd.IsSet("bytes") {
				l, err := wake.ReadList(ctx, st)
				if err != nil {
					return err
				}
				if !l.Empty() {
					return printWakeList(ctx, cmd, st, p, cwd, l)
				}
				if cmd.IsSet("dump") {
					return fmt.Errorf("--dump: there is no wake list; `chatlog wake load FILE` writes one (docs/wake.md)")
				}
			}
			d, err := wake.Digest(ctx, st, cmd.String("kind"), p, time.Now(), cmd.Int("bytes"))
			if err != nil {
				return err
			}
			fmt.Print(d)
			return nil
		},
	}
}

// printWakeList prints the wake list read: the outline to read from, or with --dump the pages written and where.
func printWakeList(ctx context.Context, cmd *cli.Command, st *store.Store, p, cwd string, l wake.List) error {
	home, _ := os.UserHomeDir()
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real
	}
	rep, err := wake.Build(ctx, st, p, l, home, cwd)
	if err != nil {
		return err
	}
	sit, err := st.KVRead(ctx, p, nil, []string{know.SitPrefix}, time.Time{})
	if err != nil {
		return err
	}
	owed, err := wake.OwedWith(ctx, st, sit)
	if err != nil {
		return err
	}
	var groups []string
	for _, a := range owed {
		groups = append(groups, a.Group)
	}
	dir := cmd.String("dump")
	if dir == "" {
		fmt.Print(rep.Outline(groups...))
		return nil
	}
	pages, err := wake.Dump(dir, rep.Content())
	if err != nil {
		return err
	}
	fmt.Printf("== PAGES (at most %d bytes each): read ALL of them in ONE turn, in parallel\n", wake.PageMax)
	for _, pg := range pages {
		fmt.Printf("  %s  %d bytes\n", pg.Path, pg.Bytes)
	}
	fmt.Printf("== ANSWER with (the counts you really read, if they differ): %s\n%s", rep.Loaded(), wake.Finish(groups...))
	return nil
}

func wakeLoadCmd() *cli.Command {
	return &cli.Command{
		Name:      "load",
		ArgsUsage: "FILE|-",
		Usage:     "write the wake list from a file (file PATH, prefix P, step TEXT, note KEY VALUE per line) into the global notes, replacing the list before",
		Flags:     []cli.Flag{&cli.BoolFlag{Name: "dry-run", Usage: "say what would be written and deleted, write nothing"}},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			var r io.Reader = os.Stdin
			if f := cmd.Args().First(); f != "" && f != "-" {
				fh, err := os.Open(f)
				if err != nil {
					return err
				}
				defer fh.Close()
				r = fh
			} else if f == "" {
				return fmt.Errorf("give a file, or - for the standard input")
			}
			l, notes, err := wake.ParseListFile(r)
			if err != nil {
				return err
			}
			set := l.Keys()
			for _, n := range notes {
				set[n.Key] = n.Value
			}
			st, err := openStore(cmd, 10*time.Second)
			if err != nil {
				return err
			}
			defer st.Close()
			del, err := wake.Stale(ctx, st, set)
			if err != nil {
				return err
			}
			verbs := map[bool]string{true: "would ", false: ""}[cmd.Bool("dry-run")]
			keys := make([]string, 0, len(set))
			for k := range set {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				fmt.Printf("%swrite %s\n", verbs, k)
			}
			for _, k := range del {
				fmt.Printf("%sdelete %s\n", verbs, k)
			}
			if cmd.Bool("dry-run") {
				return nil
			}
			_, err = st.KVWrite(ctx, store.GlobalProject, set, del)
			return err
		},
	}
}

func seedCmd() *cli.Command {
	return &cli.Command{
		Name:  "seed",
		Usage: "write the starter notes, global (the playbook's rules under rule/ tool/ legend/, and one example each of hook/ and sit/) where a key is absent; safe to repeat",
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
			_, err = st.KVWrite(ctx, store.GlobalProject, set, nil)
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
	have, err := st.KVHas(ctx, store.GlobalProject, keys)
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
