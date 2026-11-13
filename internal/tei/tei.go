// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package tei is the client of Text Embeddings Inference: /embed and /rerank over HTTP. An empty URL turns that step off.
package tei

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
	"unicode/utf8"
)

// MaxInputBytes clips what is sent. The model truncates at 8192 tokens anyway (auto_truncate), and TEI refuses a body over 2 MB.
const MaxInputBytes = 64 << 10

// ErrOff is returned at once when no URL is configured for the call: the step is off, not failing.
var ErrOff = errors.New("not configured")

// Client talks to one embedder and one reranker.
type Client struct {
	EmbedURL, RerankURL string
	HTTP                *http.Client
	Attempts            int // bounded retries of a refused connection or a 5xx; a timeout is not retried
}

// New returns a client with bounded retries.
func New(embedURL, rerankURL string) *Client {
	return &Client{EmbedURL: embedURL, RerankURL: rerankURL, HTTP: &http.Client{Timeout: 120 * time.Second}, Attempts: 3}
}

// Clip returns s within n bytes, on a rune boundary.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

type statusError struct {
	code int
	msg  string
}

func (e statusError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.code, e.msg) }

func (c *Client) post(ctx context.Context, url string, body, into any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	var last error
	for try := 0; try < c.Attempts; try++ {
		if try > 0 {
			select {
			case <-time.After(time.Duration(try) * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		last = c.once(ctx, url, b, into)
		if last == nil {
			return nil
		}
		if ne := net.Error(nil); errors.As(last, &ne) && ne.Timeout() {
			return last // the server may still be computing it: a resend doubles the work
		}
		if se, ok := last.(statusError); ok && se.code < 500 {
			return last // a refusal: the same request gets the same answer
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return last
}

func (c *Client) once(ctx context.Context, url string, body []byte, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return statusError{resp.StatusCode, string(msg)}
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// Embed returns one vector per text, in order.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if c.EmbedURL == "" {
		return nil, fmt.Errorf("embedder %w", ErrOff)
	}
	in := make([]string, len(texts))
	for i, t := range texts {
		in[i] = Clip(t, MaxInputBytes)
	}
	var out [][]float32
	if err := c.post(ctx, c.EmbedURL+"/embed", map[string]any{"inputs": in, "truncate": true}, &out); err != nil {
		return nil, err
	}
	if len(out) != len(in) {
		return nil, fmt.Errorf("embed: %d vectors for %d texts", len(out), len(in))
	}
	return out, nil
}

// Rerank returns one score per text, by the text's position.
func (c *Client) Rerank(ctx context.Context, query string, texts []string) ([]float32, error) {
	if c.RerankURL == "" {
		return nil, fmt.Errorf("reranker %w", ErrOff)
	}
	in := make([]string, len(texts))
	for i, t := range texts {
		in[i] = Clip(t, MaxInputBytes)
	}
	var out []struct {
		Index int     `json:"index"`
		Score float32 `json:"score"`
	}
	if err := c.post(ctx, c.RerankURL+"/rerank", map[string]any{"query": query, "texts": in, "truncate": true}, &out); err != nil {
		return nil, err
	}
	scores := make([]float32, len(in))
	if len(out) != len(in) {
		return nil, fmt.Errorf("rerank: %d scores for %d texts", len(out), len(in))
	}
	for _, r := range out {
		if r.Index < 0 || r.Index >= len(in) {
			return nil, fmt.Errorf("rerank: index %d of %d", r.Index, len(in))
		}
		scores[r.Index] = r.Score
	}
	return scores, nil
}
