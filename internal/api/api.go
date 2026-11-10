// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package api is the socket: HTTP over a unix socket between `chatlog server` and everything that asks it. Standard library only.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/project"
	"github.com/atvirokodosprendimai/chatlog/internal/search"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

// GetDefault is how many bytes of an exchange one chat_get returns.
const GetDefault = 30000

// TailDefault and TailMax bound the bytes of exchange text one chat_tail returns.
const (
	TailDefault = 30000
	TailMax     = 200000
)

// Every kv request names the project it acts for (an alias, a short sha, a key; the MCP server sends its own): the
// project's notes and the global ones; "*" reads every project, never writes. A request that names none is refused, so a
// client from before projects fails loudly instead of writing into the wrong one.

// KVReadRequest names keys and prefixes; At, RFC3339, reads the notes as they stood then. LinkedTo adds the keys linked
// to that object; Follow 1 lists each key's edges out and in, one hop.
type KVReadRequest struct {
	Project  string   `json:"project"`
	Cwd      string   `json:"cwd,omitempty"` // the caller\'s directory, read as the CLI reads it when Project is empty
	Keys     []string `json:"keys,omitempty"`
	Prefixes []string `json:"prefixes,omitempty"`
	At       string   `json:"at,omitempty"`
	LinkedTo string   `json:"linked_to,omitempty"`
	Follow   int      `json:"follow,omitempty"`
}

// KVWriteRequest sets and deletes keys in one transaction.
type KVWriteRequest struct {
	Project string            `json:"project"`
	Cwd     string            `json:"cwd,omitempty"` // the caller\'s directory, read as the CLI reads it when Project is empty
	Set     map[string]string `json:"set,omitempty"`
	Del     []string          `json:"del,omitempty"`
}

// KVLinkRequest adds and deletes links in one transaction.
type KVLinkRequest struct {
	Project string       `json:"project"`
	Cwd     string       `json:"cwd,omitempty"` // the caller\'s directory, read as the CLI reads it when Project is empty
	Add     []store.Link `json:"add,omitempty"`
	Del     []store.Link `json:"del,omitempty"`
}

// KVLinkResponse says how many links were added and deleted.
type KVLinkResponse struct {
	Added   int `json:"added"`
	Deleted int `json:"deleted"`
}

// KVWriteResponse says how many keys were written and which values are past the soft limit.
type KVWriteResponse struct {
	Written  int      `json:"written"`
	Warnings []string `json:"warnings,omitempty"`
}

// KVSearchRequest finds notes by meaning; Prefix narrows to a subtree of keys.
type KVSearchRequest struct {
	Project string `json:"project"`
	Cwd     string `json:"cwd,omitempty"` // the caller\'s directory, read as the CLI reads it when Project is empty
	Query   string `json:"query"`
	Prefix  string `json:"prefix,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// ArchiveRequest archives a row or, with archived false, restores it: that row only, and a why that says what was checked.
type ArchiveRequest struct {
	ID       int64  `json:"id"`
	Archived bool   `json:"archived"`
	Why      string `json:"why,omitempty"`
}

// Tail is one page of a project's latest exchanges; Next is the before of the page behind it, absent at the start.
type Tail struct {
	Items []store.Tailed `json:"items"`
	Next  int64          `json:"next_before,omitempty"`
}

// Status adds the vector count and the server's own clock to the store's numbers.
type Status struct {
	store.Status
	Vectors      int     `json:"vectors"`
	EmbedPerMin  float64 `json:"embeds_per_minute_since_start"`
	UptimeSecond int64   `json:"uptime_seconds"`
}

// Page is a bounded part of one whole exchange.
type Page struct {
	store.Row
	Text       string `json:"text"`
	TotalBytes int    `json:"total_bytes"`
	Offset     int    `json:"offset"`
	NextOffset int    `json:"next_offset,omitempty"` // absent when this is the end
}

// Handler serves the socket.
func Handler(svc *search.Service, status func(context.Context) (Status, error)) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /search", func(w http.ResponseWriter, r *http.Request) {
		var req search.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp, err := svc.Search(r.Context(), req)
		reply(w, resp, err)
	})
	mux.HandleFunc("GET /get", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if err != nil {
			http.Error(w, "id: "+err.Error(), http.StatusBadRequest)
			return
		}
		q := r.URL.Query()
		offset, _ := strconv.Atoi(q.Get("offset"))
		row, err := svc.Store.Get(r.Context(), id)
		if err != nil {
			reply(w, nil, err)
			return
		}
		reply(w, Paginate(row, offset, pageLen(q.Get("length"))), nil)
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		st, err := status(r.Context())
		reply(w, st, err)
	})
	mux.HandleFunc("POST /kv/read", func(w http.ResponseWriter, r *http.Request) {
		var req KVReadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var at time.Time
		if req.At != "" {
			var err error
			if at, err = time.Parse(time.RFC3339, req.At); err != nil {
				http.Error(w, "at: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		req.Project = requestProject(r.Context(), svc.Store, req.Project, req.Cwd)
		kv, err := kvRead(r.Context(), svc.Store, req, at)
		reply(w, kv, err)
	})
	mux.HandleFunc("POST /kv/write", func(w http.ResponseWriter, r *http.Request) {
		var req KVWriteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		warn, err := svc.Store.KVWrite(r.Context(), requestProject(r.Context(), svc.Store, req.Project, req.Cwd), req.Set, req.Del)
		reply(w, KVWriteResponse{Written: len(req.Set) + len(req.Del), Warnings: warn}, err)
	})
	mux.HandleFunc("POST /kv/link", func(w http.ResponseWriter, r *http.Request) {
		var req KVLinkRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		err := svc.Store.KVLink(r.Context(), requestProject(r.Context(), svc.Store, req.Project, req.Cwd), req.Add, req.Del)
		reply(w, KVLinkResponse{Added: len(req.Add), Deleted: len(req.Del)}, err)
	})
	mux.HandleFunc("POST /kv/search", func(w http.ResponseWriter, r *http.Request) {
		var req KVSearchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		hits, err := svc.KVSearch(r.Context(), requestProject(r.Context(), svc.Store, req.Project, req.Cwd), req.Query, req.Prefix, req.Limit)
		reply(w, hits, err)
	})
	mux.HandleFunc("POST /archive", func(w http.ResponseWriter, r *http.Request) {
		var req ArchiveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		res, err := svc.Store.SetArchived(r.Context(), req.ID, req.Archived, req.Why)
		reply(w, res, err)
	})
	mux.HandleFunc("GET /tail", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
		budget, _ := strconv.Atoi(q.Get("bytes"))
		if budget <= 0 {
			budget = TailDefault
		}
		if !store.ValidOrigin(q.Get("origin")) {
			http.Error(w, "origin: main, subagent or all", http.StatusBadRequest)
			return
		}
		items, next, err := svc.Store.Tail(r.Context(), q.Get("project"), q.Get("origin"), before, min(budget, TailMax))
		if items == nil {
			items = []store.Tailed{} // an empty page is a list, so a reader never has to tell null from nothing
		}
		reply(w, Tail{Items: items, Next: next}, err)
	})
	return mux
}

// requestProject is the project a request names. A client from before projects names none: the project of its cwd when
// it sends one, else the global notes. The MCP server sends its own.
func requestProject(_ context.Context, st *store.Store, name, cwd string) string {
	switch {
	case strings.TrimSpace(name) != "":
		return name
	case cwd != "":
		return project.FromCwd(cwd, st).Key
	}
	return store.GlobalProject
}

// kvRead is a read with its edges: the keys linked to LinkedTo join the named ones, and Follow 1 hangs each key's
// edges on it. Without both it is the plain read.
func kvRead(ctx context.Context, st *store.Store, req KVReadRequest, at time.Time) ([]store.KV, error) {
	if req.Follow != 0 && req.Follow != 1 {
		return nil, fmt.Errorf("%w: follow is 0 or 1", store.ErrRefused)
	}
	keys := req.Keys
	if req.LinkedTo != "" {
		subj, err := st.KVLinkedTo(ctx, req.Project, req.LinkedTo, at)
		if err != nil {
			return nil, err
		}
		if len(subj)+len(keys)+len(req.Prefixes) == 0 {
			return []store.KV{}, nil // nothing is linked to it: an empty list, not a refusal
		}
		keys = append(slices.Clone(keys), subj...)
	}
	kv, err := st.KVRead(ctx, req.Project, keys, req.Prefixes, at)
	if err != nil || req.Follow == 0 {
		return kv, err
	}
	names := make([]string, len(kv))
	for i, e := range kv {
		names[i] = e.Key
	}
	out, in, err := st.KVEdges(ctx, req.Project, names, at)
	if err != nil {
		return nil, err
	}
	for i := range kv {
		kv[i].Out, kv[i].In = out[kv[i].Key], in[kv[i].Key]
	}
	return kv, nil
}

// pageLen is the length a get asked for, bounded by GetDefault: a caller
// reading a byte range from its notes asks for less, never for more.
func pageLen(s string) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 && n < GetDefault {
		return n
	}
	return GetDefault
}

// Paginate cuts the range offset..offset+n out of the full text, widened to
// rune boundaries: an offset from a note can fall inside a rune.
func Paginate(row store.Row, offset, n int) Page {
	text := row.Text
	row.Text, row.Words, row.Answer = "", "", "" // the text holds both: a range carries only its bytes
	end := min(offset+n, len(text))
	for offset < len(text) && text[offset]&0xC0 == 0x80 {
		offset++
	}
	p := Page{Row: row, TotalBytes: len(text), Offset: offset}
	if offset >= len(text) {
		return p
	}
	for end < len(text) && text[end]&0xC0 == 0x80 {
		end++
	}
	p.Text = text[offset:end]
	if end < len(text) {
		p.NextOffset = end
	}
	return p
}

func reply(w http.ResponseWriter, v any, err error) {
	if errors.Is(err, store.ErrRefused) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// Client asks the socket. It holds no store: `chatlog mcp` and `chatlog search` are only this.
type Client struct {
	http *http.Client
	path string
	// Revive, when set, is called once when nothing answers on the socket (the server exited idle, or never ran);
	// the request is then tried again after the socket answers, within reviveWait.
	Revive func()
}

const reviveWait = 5 * time.Second

// NewClient returns a client of the socket at path.
func NewClient(path string) *Client {
	return &Client{path: path, http: &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}}
}

func (c *Client) do(ctx context.Context, method, path string, body, into any) error {
	var b []byte
	if body != nil {
		var err error
		if b, err = json.Marshal(body); err != nil {
			return err
		}
	}
	send := func() (*http.Response, error) {
		var rd io.Reader
		if b != nil {
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, method, "http://chatlog"+path, rd)
		if err != nil {
			return nil, err
		}
		return c.http.Do(req)
	}
	resp, err := send()
	var op *net.OpError
	if err != nil && c.Revive != nil && errors.As(err, &op) && op.Op == "dial" {
		c.Revive()
		for deadline := time.Now().Add(reviveWait); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			if conn, derr := net.DialTimeout("unix", c.path, time.Second); derr == nil {
				conn.Close()
				break
			}
		}
		resp, err = send()
	}
	if err != nil {
		return fmt.Errorf("the chatlog server does not answer, is `chatlog server` running? %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return errors.New(string(bytes.TrimSpace(msg)))
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// Search runs a search.
func (c *Client) Search(ctx context.Context, r search.Request) (search.Response, error) {
	var out search.Response
	return out, c.do(ctx, http.MethodPost, "/search", r, &out)
}

// Get fetches one page of an exchange: length bytes from offset, or the
// default page when length is 0.
func (c *Client) Get(ctx context.Context, id int64, offset, length int) (Page, error) {
	var out Page
	q := url.Values{"id": {strconv.FormatInt(id, 10)}, "offset": {strconv.Itoa(offset)}, "length": {strconv.Itoa(length)}}
	return out, c.do(ctx, http.MethodGet, "/get?"+q.Encode(), nil, &out)
}

// Status reads the status.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var out Status
	return out, c.do(ctx, http.MethodGet, "/status", nil, &out)
}

// KVRead reads the session's notes.
func (c *Client) KVRead(ctx context.Context, r KVReadRequest) ([]store.KV, error) {
	out := []store.KV{}
	return out, c.do(ctx, http.MethodPost, "/kv/read", r, &out)
}

// KVWrite writes the session's notes.
func (c *Client) KVWrite(ctx context.Context, r KVWriteRequest) (KVWriteResponse, error) {
	var out KVWriteResponse
	return out, c.do(ctx, http.MethodPost, "/kv/write", r, &out)
}

// KVLink adds and deletes links between the session's notes.
func (c *Client) KVLink(ctx context.Context, r KVLinkRequest) (KVLinkResponse, error) {
	var out KVLinkResponse
	return out, c.do(ctx, http.MethodPost, "/kv/link", r, &out)
}

// KVSearch finds the session's notes by meaning.
func (c *Client) KVSearch(ctx context.Context, r KVSearchRequest) ([]search.KVHit, error) {
	out := []search.KVHit{}
	return out, c.do(ctx, http.MethodPost, "/kv/search", r, &out)
}

// Archive archives a row, or restores it and lists the archived rows next to it.
func (c *Client) Archive(ctx context.Context, r ArchiveRequest) (store.Restored, error) {
	var out store.Restored
	return out, c.do(ctx, http.MethodPost, "/archive", r, &out)
}

// Tail fetches one page of a project's latest exchanges of one origin ("" is the user's own session).
func (c *Client) Tail(ctx context.Context, project, origin string, before int64, bytes int) (Tail, error) {
	var out Tail
	q := url.Values{"project": {project}, "origin": {origin}, "before": {strconv.FormatInt(before, 10)}, "bytes": {strconv.Itoa(bytes)}}
	return out, c.do(ctx, http.MethodGet, "/tail?"+q.Encode(), nil, &out)
}
