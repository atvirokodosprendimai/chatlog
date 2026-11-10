// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package mcpserver is the stdio MCP server, behind which mcp-go sits. It only talks to the socket.
package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/atvirokodosprendimai/chatlog/internal/api"
	"github.com/atvirokodosprendimai/chatlog/internal/playbook"
	"github.com/atvirokodosprendimai/chatlog/internal/project"
	"github.com/atvirokodosprendimai/chatlog/internal/search"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

const what = "The record of what the user and the assistant said and did: each result is one exchange, the user's message, the assistant's actions with tool output, and its answer. "

func jsonResult(v any, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

// Core is the rules of use every session follows: printed by `chatlog instructions --core` and sent as the MCP
// server's instructions at initialize, so a session knows them before its first call.
const Core = playbook.Rule + `
How to use chatlog (rules):
  1. chat_* is the verbatim record of the user's words and the assistant's answers, tool calls included; kv_* is the assistant's own notes.
  2. Before answering a short or unclear message, or a correction: chat_search with the user's own words, then say the #id found or "nothing".
  3. A hit is what was said then, not the state now: check it against the code.
  4. After a compaction or at the start of a session: kv_read prefixes [now/ waits/ rule/ fact/ tool/], then chat_tail back with next_before.
  5. kv_write takes set: {"<key>": "<one line>"} and del: [keys]; there are no key=/value= arguments.
  6. A value is a pointer, not the payload: "WAIT user: release notes draft -> chat:1234", under 200 bytes.
  7. Keys by kind: now/ state (goes stale in 12 h), waits/ what waits on the user, rule/ the user's rules with date, tool/ how a tool is used, fact/ true until changed.
  8. Relate notes with kv_link, not "-> X" inside a value.
  9. The hooks apply rules kept in kv (hook/prompt/, sit/<g>, sit/<g>/sig|words/<n>, hook/check/rule/<n>): a new rule is a kv_write; ` + "`chatlog instructions`" + ` has the shapes.
  10. chat_search, chat_tail and the wake digest read the user's own session only: what subagents did is origin=subagent (or all).
  11. kv_* act on this project's notes and the global ones; a key under global/ is global; project= names another (an alias, a short sha, a key; * reads all).
  12. chat_search archive=true also searches the archive (rows kept but hidden: old, maybe no longer true). One that still holds: chat_archive id archived=false why=<what you checked>; it restores that row only and lists its archived neighbours: check them and restore each that holds, one by one.
`

// Serve runs until stdin closes.
func Serve(c *api.Client, version string) error {
	cwd, _ := os.Getwd()              // Claude Code starts the server in the project's directory
	here := project.Resolve(cwd, nil) // the one resolver: chat_tail's default and every kv call's
	projDesc := "default " + here.Name + ": this project's notes and the global ones; a key written under global/ is global. Another project: its alias, short sha ([526abcd]) or key; \"*\" reads every project and never writes"
	s := server.NewMCPServer("chatlog", version, server.WithInstructions(Core))
	s.AddTool(mcp.NewTool("chat_search",
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDescription(what+"Search it by meaning and exact words, in any language. Each hit has score, session, time, cwd, project, the user's words, the head of the answer, and how to fetch the whole. A `degraded` field says which step did not run. A hit is what was said then, not the state now."),
		mcp.WithString("query", mcp.Required(), mcp.Description("what to find, in the user's words if you have them")),
		mcp.WithNumber("limit", mcp.Description("results, default 10")),
		mcp.WithString("since", mcp.Description("YYYY-MM-DD or RFC3339, only exchanges from then on")),
		mcp.WithString("repo", mcp.Description("part of a cwd or a project name")),
		mcp.WithString("origin", mcp.Description("whose window: default the user's own session; subagent (what subagents did) or all")),
		mcp.WithBoolean("archive", mcp.Description("true: also search the archive, rows kept but hidden by default because they are old and maybe no longer true; a hit says archived: true")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q, err := r.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(c.Search(ctx, search.Request{Query: q, Limit: r.GetInt("limit", 10), Since: r.GetString("since", ""), Repo: r.GetString("repo", ""), Origin: r.GetString("origin", "")}))
	})
	s.AddTool(mcp.NewTool("chat_get",
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDescription(what+"Fetch one whole exchange by the id from chat_search, tool output included, or one byte range of it (offset + length, as a note points: chat:ID@FROM-TO is offset FROM, length TO-FROM). Bounded: when next_offset is present, call again with it."),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("the exchange id")),
		mcp.WithNumber("offset", mcp.Description("byte offset: a previous next_offset, or the start of a range")),
		mcp.WithNumber("length", mcp.Description("bytes to return from offset; absent is a whole page")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := r.RequireInt("id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(c.Get(ctx, int64(id), r.GetInt("offset", 0), r.GetInt("length", 0)))
	})
	s.AddTool(mcp.NewTool("chat_archive",
		mcp.WithDescription("The one way to move a row in or out of the archive (rows kept but hidden from every read unless chat_search asks for archive: true). archived false restores row `id` ONLY: say in `why` what you checked that says it still holds. The answer lists its archived neighbours (the same source, the same entity of a fact, a replaced version) with id and one line, best linked first: read each, and restore the ones that still hold, one by one; nothing is restored for you. archived true puts a row back."),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("the exchange id, from chat_search")),
		mcp.WithBoolean("archived", mcp.Required(), mcp.Description("false restores, true archives")),
		mcp.WithString("why", mcp.Description("required to restore: what you checked")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := r.RequireInt("id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		archived, err := r.RequireBool("archived")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(c.Archive(ctx, api.ArchiveRequest{ID: int64(id), Archived: archived, Why: r.GetString("why", "")}))
	})
	s.AddTool(mcp.NewTool("chat_status",
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDescription("State of the record of what the user and the assistant said and did: exchanges, cut ones, pending embeds, last ingest, time range, by project."),
	), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return jsonResult(c.Status(ctx))
	})
	s.AddTool(mcp.NewTool("chat_tail",
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDescription(what+"Read it back from the end: a project's latest exchanges, oldest first, about `bytes` of text per page. After a compaction or at the start of a session, page back with next_before until you have enough of the day before. An exchange larger than the page is cut; total_bytes says so and chat_get pages it."),
		mcp.WithString("project", mcp.Description("default "+here.Name+", the project this server was started in; an alias, a short sha ([526abcd]) or a key names another")),
		mcp.WithString("origin", mcp.Description("default the user's own session; subagent or all")),
		mcp.WithNumber("before", mcp.Description("next_before of the previous page; absent is now")),
		mcp.WithNumber("bytes", mcp.Description("text per page, default 30000, at most 200000")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return jsonResult(c.Tail(ctx, r.GetString("project", here.Key), r.GetString("origin", ""), int64(r.GetInt("before", 0)), r.GetInt("bytes", api.TailDefault)))
	})
	s.AddTool(mcp.NewTool("kv_read",
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDescription("The session's own notes: one short line per key, a pointer into the other stores (chat:<id>, file:line, sha, a URL), never their payload. Read many in one call: each named key and every key under a prefix, its last value, sorted by key; the prefix \"\" is every key. `at` reads them as they stood then. Each key has `age`; a now/ or waits/ key older than 12h has `stale: true`: it was the state then, check it before saying it. follow 1: each key also lists its edges from kv_link, out {p, o} and in {s, p}. linked_to: also read every key linked to that object (\"what applies_to repo/webshop\"); with follow 1 to see how."),
		mcp.WithString("project", mcp.Description(projDesc)),
		mcp.WithArray("keys", mcp.WithStringItems(), mcp.Description("exact keys")),
		mcp.WithArray("prefixes", mcp.WithStringItems(), mcp.Description("key prefixes, e.g. now/; \"\" is every key")),
		mcp.WithString("at", mcp.Description("RFC3339: the notes as they stood then")),
		mcp.WithString("linked_to", mcp.Description("an object: a key or a pointer, e.g. repo/webshop, webshop@0074db3")),
		mcp.WithNumber("follow", mcp.Description("1 lists each key's edges one hop out and in; default 0")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return jsonResult(c.KVRead(ctx, api.KVReadRequest{Project: r.GetString("project", here.Key), Keys: r.GetStringSlice("keys", nil), Prefixes: r.GetStringSlice("prefixes", nil), At: r.GetString("at", ""), LinkedTo: r.GetString("linked_to", ""), Follow: r.GetInt("follow", 0)}))
	})
	s.AddTool(mcp.NewTool("kv_search",
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDescription("Find the session's own notes by meaning, when the key is not known: each key at its last value, nearest first, with the cosine score (compare within one result only). A note written in the last interval (30 s) may not have its vector yet."),
		mcp.WithString("project", mcp.Description(projDesc)),
		mcp.WithString("query", mcp.Required(), mcp.Description("what to find, a subject")),
		mcp.WithString("prefix", mcp.Description("only keys under it, e.g. fact/")),
		mcp.WithNumber("limit", mcp.Description("default 10")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q, err := r.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(c.KVSearch(ctx, api.KVSearchRequest{Project: r.GetString("project", here.Key), Query: q, Prefix: r.GetString("prefix", ""), Limit: r.GetInt("limit", 10)}))
	})
	s.AddTool(mcp.NewTool("kv_write",
		mcp.WithDescription("Write the session's notes, many keys in one transaction: one bad entry writes nothing, and a write never changes an old row (the history stays for kv_read at). A value is ONE line: an instruction and where its payload lies (\"WAIT user: release notes draft -> chat:1234\"). Keep it under 200 bytes: past that it is kept with a warning; past 500 the whole write is refused. A key has at most 5 segments split by /, no spaces."),
		mcp.WithString("project", mcp.Description(projDesc)),
		mcp.WithObject("set", mcp.Description("key: value, both strings"), mcp.AdditionalProperties(map[string]any{"type": "string"})),
		mcp.WithArray("del", mcp.WithStringItems(), mcp.Description("keys to delete")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		for k := range r.GetArguments() {
			if k != "set" && k != "del" && k != "project" {
				return mcp.NewToolResultError("kv_write: unknown argument " + k + `: write set: {"<key>": "<value>"} (and del: [keys])`), nil
			}
		}
		set := map[string]string{}
		raw, _ := r.GetArguments()["set"].(map[string]any)
		for k, v := range raw {
			s, ok := v.(string)
			if !ok {
				return mcp.NewToolResultError("set." + k + ": a value is a string"), nil
			}
			set[k] = s
		}
		return jsonResult(c.KVWrite(ctx, api.KVWriteRequest{Project: r.GetString("project", here.Key), Set: set, Del: r.GetStringSlice("del", nil)}))
	})
	link := map[string]any{"type": "object", "required": []string{"s", "p", "o"}, "properties": map[string]any{
		"s": map[string]any{"type": "string", "description": "a key that exists"},
		"p": map[string]any{"type": "string", "enum": store.KVPredicates},
		"o": map[string]any{"type": "string", "description": "a key or a pointer: <repo>@<sha>, chat:<id>, file:line, a URL"},
	}}
	s.AddTool(mcp.NewTool("kv_link",
		mcp.WithDescription("Relate the session's notes, instead of writing \"-> X\" inside a value: many edges s -p-> o in one transaction, one bad edge writes nothing; append-only like kv_write, a delete is a tombstone, so kv_read at sees edges as they stood. p is one of: "+strings.Join(store.KVPredicates, ", ")+". Read them back with kv_read follow 1 (both directions) or linked_to."),
		mcp.WithString("project", mcp.Description(projDesc)),
		mcp.WithArray("add", mcp.Description("edges to add"), mcp.Items(link)),
		mcp.WithArray("del", mcp.Description("edges to delete"), mcp.Items(link)),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		add, err := links(r.GetArguments()["add"])
		if err != nil {
			return mcp.NewToolResultError("add: " + err.Error()), nil
		}
		del, err := links(r.GetArguments()["del"])
		if err != nil {
			return mcp.NewToolResultError("del: " + err.Error()), nil
		}
		return jsonResult(c.KVLink(ctx, api.KVLinkRequest{Project: r.GetString("project", here.Key), Add: add, Del: del}))
	})
	return server.ServeStdio(s)
}

// links reads an array of {s, p, o} objects, as the client sent them.
func links(v any) ([]store.Link, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out []store.Link
	return out, json.Unmarshal(b, &out)
}
