// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Command chatlog keeps the chat history searchable: see `chatlog instructions`.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata" // a configured timezone works on a machine without the zone database

	"github.com/urfave/cli/v3"

	"github.com/atvirokodosprendimai/chatlog/internal/api"
	"github.com/atvirokodosprendimai/chatlog/internal/chunk"
	"github.com/atvirokodosprendimai/chatlog/internal/config"
	"github.com/atvirokodosprendimai/chatlog/internal/eval"
	"github.com/atvirokodosprendimai/chatlog/internal/hook"
	"github.com/atvirokodosprendimai/chatlog/internal/ingest"
	"github.com/atvirokodosprendimai/chatlog/internal/mcpserver"
	"github.com/atvirokodosprendimai/chatlog/internal/search"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
	"github.com/atvirokodosprendimai/chatlog/internal/tei"
	"github.com/atvirokodosprendimai/chatlog/internal/vector"
)

// version is set at release by -ldflags "-X main.version=<tag>"; a source build says dev.
var version = "dev"

const instructions = `chatlog: the record of what the user and the assistant said and did, searchable.

One exchange = the user's message, then the assistant's text, tool calls with output, and its answer.
` + "`chatlog server`" + ` reads Claude Code transcripts into SQLite and vectors and opens a unix socket;
search, status and mcp only talk to that socket. The hooks write SQLite themselves, as things happen.

  chatlog install [--dry-run] [--uninstall]   wire the hooks and the MCP server into Claude Code / take them out
  chatlog doctor                              check the install
  chatlog hook EVENT                          what the hooks run (stdin: the event's JSON); always exits 0
  chatlog ingest --all | FILE...              read transcripts once, from where each was left
  chatlog server [--source DIR]... [--once]   ingest and serve (--once: ingest, embed, exit)
  chatlog search QUERY [--since] [--repo] [--limit] [--json]
  chatlog status | events | export | forget
  chatlog import-aam FILE [--team] [--wing]... [--project-of wing=project]...   read another memory's SQLite file into the history as archived rows
  chatlog mcp                                 stdio MCP: chat_search, chat_get, chat_tail, chat_status, kv_read, kv_write, kv_search, kv_link
  chatlog eval FILE                           score the retrieval variants

Reading a search result:
  score    the reranker's when it ran (0..1), else the fused rank; compare within one result only
  id       the exchange; chat_get(id), or curl --unix-socket SOCK 'http://x/get?id=ID&offset=0'
  User:    the user's words; "to agent" when typed into a subagent's window
  degraded a step that did not run and why: without it the result is complete
A hit is what was said at that time, not the state now: check it against the code.

Project = the repository the session ran in, named by its root commit (a renamed folder is the same project); the names you use are aliases:
chatlog project ls | alias PROJECT NAME | alias -d NAME, and chatlog project prints this directory's. An alias, a short sha ([526abcd]) or the
key is accepted wherever a project is named. Notes (kv_*) belong to a project: a call sees its project's and the global ones; a key written
under global/ is global; a project's own note of a key wins over a global one; project="*" reads all. CHATLOG_PROJECT=<name> sets the project
of a call (a fleet agent in ~/work/x sets <project>-fleet). Secrets are redacted best-effort, by shape: the data dir is private.

` + mcpserver.Core + `
Origin: the exchange typed into a subagent's window is subagent, the rest main (the user's own session). chat_search, chat_tail and the
wake digest read main only, so what agents did never fills the user's context; origin=subagent or all widens a search or a tail.
Archive: a row can be archived, kept but hidden from every read (chat_search, chat_tail, the wake digest) because it is old and maybe no longer
true. chat_search archive=true also searches it, and a hit says archived: true. chat_archive id archived=false why=<what you checked> restores
that row only and answers with the archived rows one link away (id and one line each): read them and restore each that still holds, one by one.

Notes in detail (kv_*):
  key      at most 5 segments split by /, no spaces: now/release/build, rule/tests-first, tool/kv-write
  value    ONE line, an instruction and where its payload lies; over 200 bytes kept with a warning,
           over 500 the whole write is refused
  history  a write never changes an old row: kv_read at=<RFC3339> shows the notes as they stood then
  stale    keys under now/ and waits/ are marked stale after 12 h: read the source again before saying them
  edges    kv_link add [{s, p, o}]: p one of affects, blocked_by, fixed_by, decided_by, applies_to,
           supersedes, see_also; o a key or a pointer (<repo>@<sha>, chat:<id>, file:line, a URL)
  find     kv_read keys/prefixes when you know the key; kv_search query when you know only the subject

Knowledge in kv (the hooks apply these notes; a new rule is a kv_write, never a code change):
  hook/prompt/<n>          one line, printed with every prompt: "use the file tool for every file read"
  sit/<g>                  "what | move", printed when the situation fires
  sit/<g>/words/<n>        a regex over the prompt (case-insensitive): a match fires sit/<g>
  sit/<g>/sig/<n>          "TOOLRX :: INRX": TOOLRX matches the whole tool name, INRX the Bash command (else the input's
                           JSON), per line, case-insensitive: a match fires sit/<g> before the call
  kv_link rule applies_to sit/<g>   the rule's key is printed with the situation; legend/lib/<n> linked the same way
                           prints every lib/<n>/* once a session
  hook/check/rule/<n>      "TOOLRX :: INRX :: message": a turn that made such a call is told message at the next prompt
  hook/check/change/<n>    a regex over Bash commands that counts as a state change (with no kv_write: a miss)
  hook/check/bare, hook/check/skip/<n>   answers that need no recall (comma list), prompt prefixes not audited
  patterns are Go RE2 (no lookahead); one that does not read is named at the next prompt; chatlog events lists the
  situations that fired (event Karma). The meter: config hooks.context_window, hooks.sleep (default 0.6).
`

func dataDir(cmd *cli.Command) string { return cmd.String("data-dir") }

func socketPath(cmd *cli.Command) string {
	if s := cmd.String("socket"); s != "" {
		return s
	}
	p, err := config.Socket(dataDir(cmd))
	if err != nil {
		log.Printf("chatlog: %v; the socket goes in the data dir", err)
		return filepath.Join(dataDir(cmd), "chatlog.sock")
	}
	return p
}

// stampWriter starts every log line with the date and time and its offset.
type stampWriter struct{}

func (stampWriter) Write(b []byte) (int, error) {
	return fmt.Fprintf(os.Stderr, "%s %s", time.Now().Format(time.RFC3339), b)
}

func main() {
	log.SetFlags(0)
	log.SetOutput(stampWriter{})
	// A config that does not parse stops every command but hook, which must never break a session: it logs and goes on.
	// A config that does not parse stops the commands that need it. hook must never break a session; install, doctor
	// and the rest of the repair path must work to fix things: those warn and use the defaults.
	cfg, cfgErr := config.Load()
	if cfgErr != nil {
		tolerant := false
		for _, a := range os.Args[1:] {
			tolerant = tolerant || slices.Contains([]string{"hook", "install", "doctor", "instructions", "help", "--help", "-h"}, a)
		}
		if !tolerant {
			log.Printf("chatlog: %v", cfgErr)
			os.Exit(1)
		}
		if !slices.Contains(os.Args[1:], "hook") {
			log.Printf("chatlog: warning, defaults used: %v", cfgErr)
		}
	}
	root := &cli.Command{
		Name:    "chatlog",
		Version: version,
		Usage:   "the record of what the user and the assistant said and did, searchable",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "data-dir", Sources: cli.EnvVars("CHATLOG_DIR"), Value: config.DataDir()},
			&cli.StringFlag{Name: "socket", Sources: cli.EnvVars("CHATLOG_SOCKET"), Usage: "default: a short per-data-dir path under $XDG_RUNTIME_DIR, else the temp dir"},
		},
		Commands: []*cli.Command{serverCmd(cfg), searchCmd(), statusCmd(), mcpCmd(), hookCmd(cfg, cfgErr), ingestCmd(cfg), eventsCmd(),
			installCmd(), doctorCmd(cfg), wakeCmd(cfg), seedCmd(), projectCmd(), importAAMCmd(), forgetCmd(), exportCmd(), evalCmd(), {
				Name: "instructions", Usage: "what it is for and how to read its output (--core: the rules alone)",
				Flags: []cli.Flag{&cli.BoolFlag{Name: "core", Usage: "print only the rules every session follows; the same text the MCP server gives at initialize"}},
				Action: func(_ context.Context, cmd *cli.Command) error {
					if cmd.Bool("core") {
						fmt.Print(mcpserver.Core)
						return nil
					}
					fmt.Print(instructions)
					return nil
				},
			}},
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := root.Run(ctx, os.Args); err != nil {
		log.Printf("chatlog: %v", err)
		os.Exit(1)
	}
}

func serverCmd(cfg config.File) *cli.Command {
	return &cli.Command{
		Name:  "server",
		Usage: "ingest transcripts and serve the socket",
		Flags: []cli.Flag{
			sourceFlag(cfg),
			&cli.StringFlag{Name: "since", Value: cfg.Since, Usage: "exchanges older than this are not indexed (YYYY-MM-DD); empty: all"},
			&cli.StringFlag{Name: "tei", Value: cfg.TEI, Usage: "TEI embedder URL; empty: no vectors, full-text search only"},
			&cli.StringFlag{Name: "reranker", Value: cfg.Reranker, Usage: "TEI reranker URL; empty: no rerank"},
			&cli.DurationFlag{Name: "interval", Value: 30 * time.Second, Usage: "how often to look for new text"},
			&cli.BoolFlag{Name: "once", Usage: "ingest and embed once, then exit; no socket"},
			&cli.DurationFlag{Name: "idle", Value: 2 * time.Hour, Usage: "exit after this long with no search and no new text (0: never); chatlog mcp starts it again"},
			&cli.BoolFlag{Name: "stop", Usage: "stop the server of this data dir and exit"},
		},
		Action: runServer,
	}
}

func sourceFlag(cfg config.File) cli.Flag {
	src := cfg.Sources
	if len(src) == 0 {
		src = config.Sources()
	}
	return &cli.StringSliceFlag{Name: "source", Usage: "a projects directory, file or glob (repeatable)", Value: src}
}

func runServer(ctx context.Context, cmd *cli.Command) error {
	dir := dataDir(cmd)
	if cmd.Bool("stop") {
		return stopServer(dir, socketPath(cmd))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	unlock, err := lockServer(dir) // --once too: two embedders on one data dir would double the vectors' work
	if err != nil {
		return err
	}
	defer unlock()
	st, err := store.Open(filepath.Join(dir, "chatlog.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	start := time.Now()
	vec, err := vector.Open(filepath.Join(dir, "vectors"))
	if err != nil {
		return err
	}
	log.Printf("vectors loaded: %d in %v", vec.Count(), time.Since(start).Round(time.Millisecond))
	kvVec, err := vector.Open(filepath.Join(dir, "kv-vectors"))
	if err != nil {
		return err
	}
	since, err := search.ParseSince(cmd.String("since"))
	if err != nil {
		return err
	}
	client := tei.New(cmd.String("tei"), cmd.String("reranker"))
	in := &ingest.Ingester{Store: st, Vec: vec, KVVec: kvVec, TEI: client, Sources: cmd.StringSlice("source"), Since: since, Limit: chunk.DefaultLimit}

	if cmd.Bool("once") {
		return runOnce(ctx, in, vec)
	}
	ln, err := listen(socketPath(cmd))
	if err != nil {
		return err
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	startVectors := vec.Count()
	var up, seen atomic.Int64
	up.Store(time.Now().Unix())
	seen.Store(time.Now().Unix())
	svc := &search.Service{Store: st, Vec: vec, KVVec: kvVec, TEI: client}
	handler := api.Handler(svc, func(ctx context.Context) (api.Status, error) {
		s, err := st.Stats(ctx)
		secs := time.Now().Unix() - up.Load()
		rate := 0.0
		if secs > 0 {
			rate = float64(vec.Count()-startVectors) / float64(secs) * 60
		}
		return api.Status{Status: s, Vectors: vec.Count(), EmbedPerMin: rate, UptimeSecond: secs}, err
	})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(time.Now().Unix())
		handler.ServeHTTP(w, r)
	})}
	go in.Run(ctx, cmd.Duration("interval"))
	if idle := cmd.Duration("idle"); idle > 0 {
		go exitWhenIdle(ctx, cancel, st, &seen, idle, socketPath(cmd))
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	log.Printf("serving on %s", ln.Addr())
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// exitWhenIdle ends the server after idle without a request or new text: one started by `chatlog mcp` does not outlive
// the sessions that use it by more than that; the next `chatlog mcp` starts it again.
func exitWhenIdle(ctx context.Context, stop func(), st *store.Store, seen *atomic.Int64, idle time.Duration, sock string) {
	last := ""
	tick := time.NewTicker(min(idle/4, time.Minute))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if s, err := st.Stats(ctx); err == nil && s.LastIngest != last {
			if last != "" {
				seen.Store(time.Now().Unix())
			}
			last = s.LastIngest
		}
		if time.Since(time.Unix(seen.Load(), 0)) > idle {
			log.Printf("idle for %v: exiting", idle)
			stop()
			return
		}
		// a socket removed under it (a runtime dir cleared at logout) leaves a server nobody can reach that holds the
		// lock against the next one: end it
		if _, err := os.Stat(sock); err != nil {
			log.Printf("socket %s is gone: exiting", sock)
			stop()
			return
		}
	}
}

// stopServer ends the server holding the data dir's lock and waits until its socket is gone.
func stopServer(dir, sock string) error {
	f, err := os.OpenFile(filepath.Join(dir, "server.lock"), os.O_RDWR, 0o600)
	if err != nil {
		return errors.New("no server has run on this data dir")
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
		fmt.Println("no server runs on", dir)
		return nil
	}
	b, _ := io.ReadAll(f)
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return fmt.Errorf("a server holds %s but its pid is unreadable", f.Name())
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("stop %d: %w", pid, err)
	}
	for range 50 {
		if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			fmt.Printf("stopped the server (pid %d)\n", pid)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("server pid %d did not stop within 5 s", pid)
}

func runOnce(ctx context.Context, in *ingest.Ingester, vec *vector.Index) error {
	start := time.Now()
	if err := in.Scan(ctx); err != nil {
		return err
	}
	scanned := time.Since(start)
	st, err := in.Store.Stats(ctx)
	if err != nil {
		return err
	}
	log.Printf("ingest: %d chunks, %d files in %v", st.Chunks, st.Files, scanned.Round(time.Millisecond))
	start = time.Now()
	embErr := in.Embed(ctx, true)
	log.Printf("embed: %d vectors in %v", vec.Count(), time.Since(start).Round(time.Millisecond))
	return embErr
}

// listen opens the socket as 0600. A live server on it is an error; a stale file is removed.
func listen(path string) (net.Listener, error) {
	if len(path) >= 104 { // sun_path: 104 bytes on macOS, 108 on Linux, the terminating NUL included
		return nil, fmt.Errorf("socket path is %d bytes, over the 103 a unix socket allows: set CHATLOG_SOCKET (or --socket) to a shorter path: %s", len(path), path)
	}
	if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
		conn.Close()
		return nil, fmt.Errorf("a server already answers on %s", path)
	}
	os.Remove(path)
	old := syscall.Umask(0o177) // no moment exists in which the socket is open to others
	ln, err := net.Listen("unix", path)
	syscall.Umask(old)
	return ln, err
}

func searchCmd() *cli.Command {
	return &cli.Command{
		Name:      "search",
		Usage:     "the same query as chat_search",
		ArgsUsage: "QUERY",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "since"}, &cli.StringFlag{Name: "repo"},
			&cli.StringFlag{Name: "origin", Usage: "default the user's own session; subagent or all"},
			&cli.BoolFlag{Name: "archive", Usage: "also search the archive: rows kept but hidden by default, old and maybe no longer true"},
			&cli.IntFlag{Name: "limit", Value: 10}, &cli.BoolFlag{Name: "json"},
			&cli.StringFlag{Name: "mode"}, &cli.StringFlag{Name: "form"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			q := strings.Join(cmd.Args().Slice(), " ")
			resp, err := api.NewClient(socketPath(cmd)).Search(ctx, search.Request{Query: q, Limit: cmd.Int("limit"),
				Since: cmd.String("since"), Repo: cmd.String("repo"), Mode: cmd.String("mode"), Form: cmd.String("form"), Origin: cmd.String("origin"), Archive: cmd.Bool("archive")})
			if err != nil {
				return err
			}
			if cmd.Bool("json") {
				return json.NewEncoder(os.Stdout).Encode(resp)
			}
			printHits(resp)
			return nil
		},
	}
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	return tei.Clip(s, n)
}

func printHits(r search.Response) {
	fmt.Printf("%d hits, %s, %d ms\n", len(r.Hits), r.Mode, r.TookMs)
	for _, d := range r.Degraded {
		fmt.Printf("DEGRADED %s\n", d)
	}
	for _, h := range r.Hits {
		fmt.Printf("\n%.3f  id=%d  %s  %s  %s%s\n", h.Score, h.ID, h.Time.Local().Format("2006-01-02 15:04"), h.Project, h.Session, map[bool]string{true: "  [archived]", false: ""}[h.Archived])
		if h.Agent != "" {
			fmt.Printf("  to agent %s\n", h.Agent)
		}
		fmt.Printf("  User: %s\n  A: %s\n", oneLine(h.Words, 300), oneLine(h.AnswerHead, 300))
	}
}

func statusCmd() *cli.Command {
	return &cli.Command{
		Name: "status", Usage: "chunks, last ingest, sessions, pending embeds",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			st, err := api.NewClient(socketPath(cmd)).Status(ctx)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(st)
		},
	}
}

func mcpCmd() *cli.Command {
	return &cli.Command{
		Name: "mcp", Usage: "stdio MCP server; talks only to the socket, and starts the server when nothing answers on it (CHATLOG_AUTOSTART=0: never)",
		Action: func(_ context.Context, cmd *cli.Command) error {
			c := api.NewClient(socketPath(cmd))
			if os.Getenv("CHATLOG_AUTOSTART") != "0" {
				startServer(cmd)
				// the server exits after --idle; a session left open longer starts it again at its next call
				c.Revive = func() { startServer(cmd) }
			}
			return mcpserver.Serve(c, version)
		},
	}
}

// lockServer takes the data dir's server lock, so two servers never ingest, embed and listen on one data dir.
func lockServer(dir string) (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(dir, "server.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another chatlog server holds %s (chatlog server --stop ends it): %w", f.Name(), err)
	}
	// the pid, for --stop
	if err := f.Truncate(0); err == nil {
		f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return func() { f.Close() }, nil
}

// startServer starts `chatlog server` in the background when nothing answers on the socket, so the history is searchable
// whenever Claude Code runs, with no service to install. The server's lock makes a second start exit at once.
func startServer(cmd *cli.Command) {
	if c, err := net.DialTimeout("unix", socketPath(cmd), 200*time.Millisecond); err == nil {
		c.Close()
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	dir := dataDir(cmd)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	logPath := filepath.Join(dir, "server.log")
	hook.Rotate(logPath)
	out, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer out.Close()
	args := []string{"--data-dir", dir}
	if s := cmd.String("socket"); s != "" {
		args = append(args, "--socket", s)
	}
	c := exec.Command(exe, append(args, "server")...)
	c.Stdout, c.Stderr = out, out
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // its own session: it outlives this MCP server and its terminal
	if c.Start() == nil {
		c.Process.Release()
	}
}

// variant is one retrieval setting the eval compares.
type variant struct {
	name, mode, form string
}

var variants = []variant{
	{"vector", search.ModeVector, ""},
	{"fts", search.ModeFTS, ""},
	{"fused", search.ModeFused, ""},
	{"rerank head", search.ModeRerank, search.FormHead},
	{"rerank words-answer", search.ModeRerank, search.FormWordsAnswer},
	{"rerank window", search.ModeRerank, search.FormWindow},
}

func evalCmd() *cli.Command {
	return &cli.Command{
		Name: "eval", Usage: "hit@1, hit@5, MRR and p95 of each retrieval variant", ArgsUsage: "FILE",
		Flags: []cli.Flag{&cli.StringFlag{Name: "since", Usage: "scope of the search"}, &cli.StringFlag{Name: "repo", Usage: "scope; / also takes the base project"}, &cli.BoolFlag{Name: "misses", Usage: "print each question's rank per variant"}},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			b, err := os.ReadFile(cmd.Args().First())
			if err != nil {
				return err
			}
			var qs []eval.Question
			if err := json.Unmarshal(b, &qs); err != nil {
				return err
			}
			c := api.NewClient(socketPath(cmd))
			fmt.Printf("%d questions, scope since=%q\n%-26s %6s %6s %6s %8s %s\n", len(qs), cmd.String("since"), "variant", "hit@1", "hit@5", "MRR", "p95", "degraded")
			for _, v := range variants {
				var score eval.Score
				degraded := 0
				for _, q := range qs {
					req := search.Request{Query: q.Q, Limit: 20, Since: cmd.String("since"), Repo: cmd.String("repo"), Mode: v.mode, Form: v.form}
					start := time.Now()
					resp, err := c.Search(ctx, req)
					if err != nil {
						return fmt.Errorf("%s %q: %w", v.name, q.Q, err)
					}
					degraded += len(resp.Degraded)
					got := make([]string, len(resp.Hits))
					for i, h := range resp.Hits {
						got[i] = h.UUID
					}
					rank := eval.Rank(got, q.Expect)
					score.Add(rank, time.Since(start))
					if cmd.Bool("misses") {
						fmt.Printf("  %-26s rank %-3s %s\n", v.name, strconv.Itoa(rank), oneLine(q.Q, 60))
					}
				}
				r := score.Result()
				fmt.Printf("%-26s %6.2f %6.2f %6.3f %8v %d\n", v.name, r.Hit1, r.Hit5, r.MRR, r.P95.Round(time.Millisecond), degraded)
			}
			return nil
		},
	}
}
