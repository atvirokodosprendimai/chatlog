// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package search answers a query: vector list + full-text list -> fused -> reranked when the reranker answers.
// f(query, stores) -> hits + what was degraded. Nothing here writes, and it never waits for an ingest.
package search

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/atvirokodosprendimai/chatlog/internal/store"
	"github.com/atvirokodosprendimai/chatlog/internal/tei"
	"github.com/atvirokodosprendimai/chatlog/internal/vector"
)

// Modes of retrieval; the default is decided by `chatlog eval`, see the README.
const (
	ModeVector = "vector"
	ModeFTS    = "fts"
	ModeFused  = "fused"
	ModeRerank = "rerank"
)

// What the reranker reads. It reads from the start of a document, so what stands there decides.
const (
	FormHead        = "head"         // the embedded text as it is: words, answer, then tool steps
	FormWordsAnswer = "words-answer" // only the user's words and the answer
	FormWindow      = "window"       // words, answer and the place of the whole exchange that best holds the query's words
)

const (
	rrfK           = 60
	pool           = 50
	vectorFetch    = 500 // filters are applied after: a wide list keeps them from emptying the pool
	maxRerankBytes = 6000
	queryEmbedWait = 5 * time.Second
	rerankWait     = 15 * time.Second
	wordsHead      = 2000
	answerHead     = 2000
)

// Defaults picked by the eval; see README.
var (
	DefaultMode = ModeRerank
	DefaultForm = FormWindow
)

// Request is one search.
type Request struct {
	Query  string `json:"query"`
	Limit  int    `json:"limit,omitempty"`
	Since  string `json:"since,omitempty"` // YYYY-MM-DD or RFC3339
	Repo   string `json:"repo,omitempty"`
	Mode   string `json:"mode,omitempty"` // eval's handle on the variants
	Form   string `json:"form,omitempty"`
	Origin string `json:"origin,omitempty"` // "" or main: the user's own session; subagent; all
}

// Hit is one result. Fetch says how to read the whole exchange.
type Hit struct {
	ID         int64     `json:"id"`
	UUID       string    `json:"uuid"`
	Score      float64   `json:"score"`
	Session    string    `json:"session"`
	Agent      string    `json:"agent,omitempty"`
	Time       time.Time `json:"time"`
	Cwd        string    `json:"cwd"`
	Project    string    `json:"project"`
	Words      string    `json:"words"`
	AnswerHead string    `json:"answer_head"`
	Fetch      string    `json:"fetch"`
}

// Response is the result; Degraded names every step that did not run, and why.
type Response struct {
	Hits     []Hit    `json:"hits"`
	Mode     string   `json:"mode"`
	Degraded []string `json:"degraded,omitempty"`
	TookMs   int64    `json:"took_ms"`
}

// Service holds the stores.
type Service struct {
	Store *store.Store
	Vec   *vector.Index
	KVVec *vector.Index
	TEI   *tei.Client
}

// KVHit is a note found by meaning: its last value, and the cosine similarity of "key: value" to the query.
type KVHit struct {
	store.KV
	Score float32 `json:"score"`
}

// KVSearch returns the notes nearest the query, each key at its last value; prefix narrows to a subtree. It sees the notes
// of project and the global ones ("*": every project's).
func (s *Service) KVSearch(ctx context.Context, project, q, prefix string, limit int) ([]KVHit, error) {
	if strings.TrimSpace(q) == "" {
		return nil, errors.New("empty query")
	}
	if _, err := s.Store.ResolveProject(ctx, project); err != nil { // before the embedder is asked: a request that names none is no work
		return nil, err
	}
	if limit <= 0 {
		limit = 10
	}
	ectx, cancel := context.WithTimeout(ctx, queryEmbedWait)
	defer cancel()
	vecs, err := s.TEI.Embed(ectx, []string{q})
	if err != nil {
		return nil, err
	}
	// every vector is asked for: the notes of a hundred projects share one index, and what is not this project's is dropped after
	hits, err := s.KVVec.Query(ctx, vecs[0], s.KVVec.Count())
	if err != nil {
		return nil, err
	}
	rids := make([]int64, len(hits))
	for i, h := range hits {
		rids[i] = h.ID
	}
	live, err := s.Store.KVLatest(ctx, project, rids, prefix)
	if err != nil {
		return nil, err
	}
	out := []KVHit{}
	for _, h := range hits {
		if e, ok := live[h.ID]; ok && len(out) < limit {
			out = append(out, KVHit{KV: e, Score: h.Score})
		}
	}
	return out, nil
}

// Fuse is reciprocal rank fusion, pure: ids by the sum of 1/(k+rank), ties by id so a run repeats.
func Fuse(lists ...[]int64) []int64 {
	score := map[int64]float64{}
	for _, l := range lists {
		for i, id := range l {
			score[id] += 1 / float64(rrfK+i+1)
		}
	}
	out := make([]int64, 0, len(score))
	for id := range score {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool {
		if score[out[i]] != score[out[j]] {
			return score[out[i]] > score[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

// Window returns the n bytes of text holding the most occurrences of terms; empty when none occurs.
func Window(text string, terms []string, n int) string {
	lower := strings.ToLower(text)
	var pos []int
	for _, t := range terms {
		t = strings.ToLower(t)
		if len(t) < 2 {
			continue
		}
		for from := 0; ; {
			i := strings.Index(lower[from:], t)
			if i < 0 {
				break
			}
			pos = append(pos, from+i)
			from += i + len(t)
		}
	}
	if len(pos) == 0 {
		return ""
	}
	sort.Ints(pos)
	best, bestAt, lo := 0, pos[0], 0
	for hi, p := range pos {
		for p-pos[lo] >= n {
			lo++
		}
		if hi-lo+1 > best {
			best, bestAt = hi-lo+1, pos[lo]
		}
	}
	start := max(0, bestAt-n/8)
	end := min(len(lower), start+n)
	return strings.ToValidUTF8(lower[start:end], "")
}

type candidate struct {
	words, answer, embed, text string
}

// rerankText is the document the reranker judges, by form. Separate from the embedded text on purpose.
func rerankText(form string, c candidate, terms []string) string {
	wa := "User: " + tei.Clip(c.words, wordsHead) + "\n\nA: " + tei.Clip(c.answer, answerHead)
	switch form {
	case FormHead:
		return tei.Clip(c.embed, maxRerankBytes)
	case FormWordsAnswer:
		return tei.Clip(wa, maxRerankBytes)
	}
	room := maxRerankBytes - len(wa) - 2
	if room <= 0 {
		return tei.Clip(wa, maxRerankBytes)
	}
	return wa + "\n\n" + Window(c.text, terms, room)
}

// ParseSince reads a date or a moment; empty is no bound.
func ParseSince(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02", s, time.Local)
}

// Search runs one request.
func (s *Service) Search(ctx context.Context, r Request) (Response, error) {
	start := time.Now()
	if strings.TrimSpace(r.Query) == "" {
		return Response{}, errors.New("empty query")
	}
	since, err := ParseSince(r.Since)
	if err != nil {
		return Response{}, fmt.Errorf("since: %w", err)
	}
	limit := r.Limit
	if limit <= 0 {
		limit = 10
	}
	mode, form := r.Mode, r.Form
	if mode == "" {
		mode = DefaultMode
	}
	if form == "" {
		form = DefaultForm
	}
	if !store.ValidOrigin(r.Origin) {
		return Response{}, fmt.Errorf("origin %q: main, subagent or all", r.Origin)
	}
	f := store.Filter{Since: since, Repo: r.Repo, Origin: r.Origin}
	if r.Repo != "" { // an alias, a short sha or a key names the project; any other word is part of a path
		key, names, found, err := s.Store.FindProject(ctx, r.Repo)
		if err != nil {
			return Response{}, err
		}
		if found {
			f.Key, f.Names = key, names
		}
	}
	resp := Response{Mode: mode}

	var vecList, ftsList []int64
	if mode != ModeFTS {
		vecList, err = s.vectorList(ctx, r.Query, f)
		if err != nil {
			if mode == ModeVector {
				return resp, err
			}
			resp.Degraded = append(resp.Degraded, "vector: "+err.Error())
		}
	}
	if mode != ModeVector {
		ftsList, err = s.Store.FTS(ctx, r.Query, pool, f)
		if err != nil {
			return resp, fmt.Errorf("fts: %w", err)
		}
	}
	ids := Fuse(vecList, ftsList)
	if mode == ModeRerank {
		ids = ids[:min(len(ids), pool)]
	} else {
		ids = ids[:min(len(ids), limit)]
	}
	rows, err := s.Store.Rows(ctx, ids, f, form == FormHead && mode == ModeRerank, form == FormWindow && mode == ModeRerank)
	if err != nil {
		return resp, err
	}
	scores := make(map[int64]float64, len(ids))
	for i, id := range ids {
		scores[id] = 1 / float64(rrfK+i+1)
	}
	if mode == ModeRerank && len(ids) > 0 {
		if err := s.rerank(ctx, r, form, ids, rows, scores); err != nil {
			resp.Degraded = append(resp.Degraded, "rerank: "+err.Error())
		} else {
			sort.SliceStable(ids, func(i, j int) bool { return scores[ids[i]] > scores[ids[j]] })
		}
	}
	for _, id := range ids[:min(len(ids), limit)] {
		row, ok := rows[id]
		if !ok {
			continue
		}
		resp.Hits = append(resp.Hits, Hit{ID: id, UUID: row.UUID, Score: scores[id], Session: row.Session, Agent: row.Agent, Time: row.Time, Cwd: row.Cwd,
			Project: row.Project, Words: tei.Clip(row.Words, 1000), AnswerHead: tei.Clip(row.Answer, 500), Fetch: fmt.Sprintf("chat_get(id=%d)", id)})
	}
	resp.TookMs = time.Since(start).Milliseconds()
	return resp, nil
}

// vectorList returns ids that pass the filter, nearest first.
func (s *Service) vectorList(ctx context.Context, q string, f store.Filter) ([]int64, error) {
	ectx, cancel := context.WithTimeout(ctx, queryEmbedWait)
	defer cancel()
	vecs, err := s.TEI.Embed(ectx, []string{q})
	if err != nil {
		return nil, err
	}
	hits, err := s.Vec.Query(ctx, vecs[0], vectorFetch)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	rows, err := s.Store.Rows(ctx, ids, f, false, false)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, pool)
	for _, id := range ids {
		if _, ok := rows[id]; ok && len(out) < pool {
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *Service) rerank(ctx context.Context, r Request, form string, ids []int64, rows map[int64]store.Row, scores map[int64]float64) error {
	terms := store.Terms(r.Query)
	docs := make([]string, 0, len(ids))
	kept := make([]int64, 0, len(ids))
	for _, id := range ids {
		row, ok := rows[id]
		if !ok {
			continue
		}
		kept = append(kept, id)
		docs = append(docs, rerankText(form, candidate{words: row.Words, answer: row.Answer, embed: row.Embed, text: row.Text}, terms))
	}
	rctx, cancel := context.WithTimeout(ctx, rerankWait)
	defer cancel()
	got, err := s.TEI.Rerank(rctx, r.Query, docs)
	if err != nil {
		return err
	}
	for i, id := range kept {
		scores[id] = float64(got[i])
	}
	return nil
}
