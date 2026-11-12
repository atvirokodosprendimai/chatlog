// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package mcpserver is the stdio MCP server, behind which mcp-go sits. It only talks to the socket.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
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
  1. chat_* is the record, tool calls included; kv_* your own notes: kv_write set {"<key>": "<one line>"}, del [keys].
  2. A short or unclear message, or a correction: chat_search the user's own words first, say the #id or "nothing".
  3. After a compaction: kv_read prefixes [now/ waits/ rule/ fact/ tool/], then chat_tail back with next_before.
  4. A value is a pointer under 200 bytes ("WAIT user: notes draft -> chat:1234"); relate notes with kv_link.
  5. Keys: now/ state (stale in 12 h), waits/ what waits on the user, rule/ the user's rules, tool/, fact/ true until changed.
  6. Hook rules live in kv (hook/prompt/, sit/<g>, hook/check/rule/<n>); ` + "`chatlog instructions`" + ` has the shapes.
  7. chat_* read the user's own session; origin=subagent or all widens; archive=true adds the archive (chat_archive restores).
  8. kv_* act on this project's notes and global/; project= names another (alias, short sha, key; * all).
`

// Serve runs until stdin closes.
func Serve(c *api.Client, version string) error {
	return server.ServeStdio(newServer(c, version))
}

// newServer is the server with its tools; split from Serve so a test can read what a client would be told.
func newServer(c *api.Client, version string) *server.MCPServer {
	cwd, _ := os.Getwd()              // Claude Code starts the server in the project's directory
	here := project.Resolve(cwd, nil) // the one resolver: chat_tail's default and every kv call's
	projDesc := "default " + here.Name + ": this project's notes and the global ones (a key under global/ is global); another project: its alias, short sha ([526abcd]) or key; \"*\" reads all, never writes"
	s := server.NewMCPServer("chatlog", version, server.WithInstructions(Core))
	s.AddTool(mcp.NewTool("chat_search",
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDescription(what+"Search it by meaning and exact words, in any language. Each hit has score, session, time, cwd, project, the user's words, the head of the answer, and how to fetch the whole. A `degraded` field says which step did not run. A hit is what was said then, not the state now."),
		mcp.WithString("query", mcp.Required(), mcp.Description("what to find, in the user's words if you have them")),
		mcp.WithNumber("limit", mcp.Description("results, default 10")),
		mcp.WithString("since", mcp.Description("YYYY-MM-DD or RFC3339, only exchanges from then on")),
		mcp.WithString("repo", mcp.Description("part of a cwd or a project name")),
		mcp.WithString("until", mcp.Description("YYYY-MM-DD or RFC3339, only exchanges before then (exclusive: since and until tile); a date is the start of that day")),
		mcp.WithString("role", mcp.Enum("user", "assistant", "any"), mcp.Description("user: the query must match the user's words only; assistant: the answer only; any (default): both. Matches by words in that role's text, not by meaning alone")),
		mcp.WithString("origin", mcp.Description("whose window: default the user's own session; subagent (what subagents did) or all")),
		mcp.WithBoolean("archive", mcp.Description("true: also search the archive, rows kept but hidden by default because they are old and maybe no longer true; a hit says archived: true")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q, err := r.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(c.Search(ctx, search.Request{Query: q, Limit: r.GetInt("limit", 10), Since: r.GetString("since", ""), Until: r.GetString("until", ""), Role: r.GetString("role", ""), Repo: r.GetString("repo", ""), Origin: r.GetString("origin", ""), Archive: r.GetBool("archive", false)}))
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
		mcp.WithDescription("The session's own notes: one short line per key, a pointer into the other stores (chat:<id>, file:line, sha, a URL), never the payload. Read many in one call: each named key and every key under a prefix (\"\" is all), its last value, sorted by key. Each key has `age`; `stale: true` is a past expires_at, or a now/, waits/, task/, todo/ or promise/ key over 12h (fact/, known/, rule/, tool/ never): the state then, check the source, rewrite or delete. A long note shows `has_body`; its text comes with full 1. Edges, deleted notes, cheap listings, changes since a time: see the parameters; the rest in `chatlog instructions`."),
		mcp.WithString("project", mcp.Description(projDesc)),
		mcp.WithArray("keys", mcp.WithStringItems(), mcp.Description("exact keys")),
		mcp.WithArray("prefixes", mcp.WithStringItems(), mcp.Description("key prefixes, e.g. now/; \"\" is every key")),
		mcp.WithString("at", mcp.Description("RFC3339: the notes as they stood then; age counts to it")),
		mcp.WithString("linked_to", mcp.Description("an object (a key or a pointer, e.g. repo/webshop): also reads every key linked to it")),
		mcp.WithNumber("follow", mcp.Description("1: each key lists its kv_link edges, out {p, o} and in {s, p}; default 0")),
		mcp.WithNumber("with_deleted", mcp.Description("1 also returns deleted keys at their last value, with deleted_at; default 0")),
		mcp.WithNumber("stale", mcp.Description("1 keeps only the stale keys (prefixes [\"\"] for all), to rewrite or delete each; default 0")),
		mcp.WithNumber("full", mcp.Description("1 also returns each note's body (has_body says which have one); default 0")),
		mcp.WithNumber("keys_only", mcp.Description("1: key, time, age, bytes, stale, has_body and no value, the cheap look; default 0")),
		mcp.WithString("changed_since", mcp.Description("RFC3339 or a span back (1h, 7d): only keys written or deleted since; deleted ones need with_deleted 1")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return jsonResult(c.KVRead(ctx, api.KVReadRequest{Project: r.GetString("project", here.Key), Keys: r.GetStringSlice("keys", nil), Prefixes: r.GetStringSlice("prefixes", nil), At: r.GetString("at", ""), LinkedTo: r.GetString("linked_to", ""), Follow: r.GetInt("follow", 0), WithDeleted: r.GetInt("with_deleted", 0) == 1, Stale: r.GetInt("stale", 0),
			KeysOnly: r.GetInt("keys_only", 0) == 1, Full: r.GetInt("full", 0) == 1, ChangedSince: r.GetString("changed_since", "")}))
	})
	s.AddTool(mcp.NewTool("kv_search",
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDescription("Find the session's own notes by meaning, when the key is not known: each key at its last value, nearest first, with the cosine score (compare within one result only). Only the head (the set value) is indexed, never a body; a hit shows has_body. A note written in the last interval (30 s) may not have its vector yet. with_deleted 1 also finds deleted notes, at the value they had, marked deleted_at."),
		mcp.WithString("project", mcp.Description(projDesc)),
		mcp.WithString("query", mcp.Required(), mcp.Description("what to find, a subject")),
		mcp.WithString("prefix", mcp.Description("only keys under it, e.g. fact/")),
		mcp.WithNumber("limit", mcp.Description("default 10")),
		mcp.WithNumber("with_deleted", mcp.Description("1 also finds deleted notes; default 0")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q, err := r.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(c.KVSearch(ctx, api.KVSearchRequest{Project: r.GetString("project", here.Key), Query: q, Prefix: r.GetString("prefix", ""), Limit: r.GetInt("limit", 10), WithDeleted: r.GetInt("with_deleted", 0) == 1}))
	})
	s.AddTool(mcp.NewTool("kv_write",
		mcp.WithDescription("Write the session's notes, many keys in one transaction: one bad entry writes nothing, and a write never changes an old row (the history stays for kv_read at). A value is ONE line, the head: an instruction and where its payload lies (\"WAIT user: release notes draft -> chat:1234\"). Keep it under 200 bytes: past that it is kept with a warning; past 500 the whole write is refused. A key has at most 5 segments split by /, no spaces. expires {\"<key>\": \"12h\" | \"7d\" | RFC3339} says when the note goes old; past it kv_read marks it stale. A delete is soft: kv_read / kv_search with_deleted 1 find it. if_absent [keys] / if_value {key: expected}: refused, whole, unless each key has no live value / holds exactly that value, checked in the same transaction; the refusal names each key with its value, time and project (claim a shared number: if_absent [\"task/adr/7\"]). body {key: text}: a long text of at most "+fmt.Sprintf("%d KiB", store.KVBodyMax/1024)+" kept with the key, whose head stays required; a rewrite without body drops it (the write warns; the history keeps it)."),
		mcp.WithString("project", mcp.Description(projDesc)),
		mcp.WithObject("set", mcp.Description("key: value, both strings"), mcp.AdditionalProperties(map[string]any{"type": "string"})),
		mcp.WithArray("del", mcp.WithStringItems(), mcp.Description("keys to delete")),
		mcp.WithObject("expires", mcp.Description("key: span (12h, 7d) or RFC3339; key in set"), mcp.AdditionalProperties(map[string]any{"type": "string"})),
		mcp.WithArray("if_absent", mcp.WithStringItems(), mcp.Description("keys that must have no live value, else nothing is written")),
		mcp.WithObject("if_value", mcp.Description("key: the value it must hold now, else nothing is written"), mcp.AdditionalProperties(map[string]any{"type": "string"})),
		mcp.WithObject("body", mcp.Description("key: long text kept with the key (key in set); kv_read full 1 reads it"), mcp.AdditionalProperties(map[string]any{"type": "string"})),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		req, err := kvWriteRequest(r, here.Key)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(c.KVWrite(ctx, req))
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
	return s
}

// kvWriteArgs are the arguments kv_write knows; any other is named in the refusal.
var kvWriteArgs = []string{"project", "set", "del", "expires", "if_absent", "if_value", "body"}

// kvWriteRequest reads kv_write's arguments strictly: an argument of the wrong type is a refusal, never dropped. A condition
// that is not understood would otherwise turn a claim into an unconditional write.
func kvWriteRequest(r mcp.CallToolRequest, here string) (api.KVWriteRequest, error) {
	var req api.KVWriteRequest
	for k := range r.GetArguments() {
		if !slices.Contains(kvWriteArgs, k) {
			return req, errors.New("kv_write: unknown argument " + k + `: write set: {"<key>": "<value>"} (and del: [keys], expires, if_absent, if_value)`)
		}
	}
	var err error
	if req.Set, err = stringMap(r, "set"); err != nil {
		return req, err
	}
	if req.Expires, err = stringMap(r, "expires"); err != nil {
		return req, err
	}
	if req.Body, err = stringMap(r, "body"); err != nil {
		return req, err
	}
	if req.IfValue, err = stringMap(r, "if_value"); err != nil {
		return req, err
	}
	if req.Del, err = stringList(r, "del"); err != nil {
		return req, err
	}
	if req.IfAbsent, err = stringList(r, "if_absent"); err != nil {
		return req, err
	}
	req.Project = r.GetString("project", here)
	return req, nil
}

// stringMap reads an object argument whose values are all strings; absent is empty, any other type an error.
func stringMap(r mcp.CallToolRequest, name string) (map[string]string, error) {
	out := map[string]string{}
	v, present := r.GetArguments()[name]
	if !present || v == nil { // a null is an absent argument
		return out, nil
	}
	raw, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New(name + ": an object of key: string")
	}
	for k, v := range raw {
		s, ok := v.(string)
		if !ok {
			return nil, errors.New(name + "." + k + ": a string")
		}
		out[k] = s
	}
	return out, nil
}

// stringList reads an array argument of strings; absent is nil, any other type an error.
func stringList(r mcp.CallToolRequest, name string) ([]string, error) {
	v, present := r.GetArguments()[name]
	if !present || v == nil { // a null is an absent argument
		return nil, nil
	}
	raw, ok := v.([]any)
	if !ok {
		return nil, errors.New(name + ": an array of strings")
	}
	out := make([]string, len(raw))
	for i, e := range raw {
		if out[i], ok = e.(string); !ok {
			return nil, errors.New(name + ": an array of strings")
		}
	}
	return out, nil
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
