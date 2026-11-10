// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/atvirokodosprendimai/chatlog/internal/project"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

func projectCmd() *cli.Command {
	return &cli.Command{
		Name: "project", Usage: "the project of this directory; ls lists every project with its aliases; alias names one",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			st, err := openStore(cmd, 5*time.Second)
			if err != nil {
				return err
			}
			defer st.Close()
			cwd, _ := os.Getwd()
			s, err := describeProject(ctx, st, cwd)
			fmt.Println(s)
			return err
		},
		Commands: []*cli.Command{{
			Name: "ls", Usage: "every project: its short key, its aliases, what it holds",
			Action: func(ctx context.Context, cmd *cli.Command) error {
				st, err := openStore(cmd, 5*time.Second)
				if err != nil {
					return err
				}
				defer st.Close()
				ps, err := st.Projects(ctx)
				for _, p := range ps {
					fmt.Println(p)
				}
				return err
			},
		}, {
			Name: "alias", ArgsUsage: "PROJECT ALIAS | -d ALIAS",
			Usage: "give a project (an alias, a short sha, a key) one more name; -d removes a name. A name stands for one project; the folder's base name is added by itself when first seen",
			Flags: []cli.Flag{&cli.BoolFlag{Name: "d", Usage: "remove the alias"}},
			Action: func(ctx context.Context, cmd *cli.Command) error {
				args := cmd.Args().Slice()
				st, err := openStore(cmd, 10*time.Second)
				if err != nil {
					return err
				}
				defer st.Close()
				switch {
				case cmd.Bool("d") && len(args) == 1:
					return st.DelAlias(ctx, args[0])
				case !cmd.Bool("d") && len(args) == 2:
					return st.AddAlias(ctx, args[0], args[1])
				}
				return fmt.Errorf("usage: chatlog project alias PROJECT ALIAS, or chatlog project alias -d ALIAS")
			},
		}},
	}
}

// describeProject says which project a directory is, as the hooks and the MCP server see it from there, and how that was found.
func describeProject(ctx context.Context, st *store.Store, cwd string) (string, error) {
	id := project.Resolve(cwd, st)
	key, err := st.ResolveProject(ctx, id.Key)
	if err != nil {
		return "", err
	}
	label := st.Label(ctx, key)
	via := "the repository's root commit, from " + id.Root
	switch {
	case id.Env:
		via = "CHATLOG_PROJECT"
	case strings.HasPrefix(id.Key, "path:"):
		via = "the path: no repository with a commit at " + id.Root
	case id.Key == "unknown":
		via = "no directory known"
	}
	return fmt.Sprintf("%s %s (key %s, found by %s)", project.Short(key), label, key, via), nil
}
