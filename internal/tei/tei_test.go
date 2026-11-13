// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package tei

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func vectors(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Inputs []string `json:"inputs"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	out := make([][]float32, len(req.Inputs))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	json.NewEncoder(w).Encode(out)
}

// A request that timed out may still be computing on the server: sending it again doubles the work.
func TestTimedOutRequestIsNotSentAgain(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release
	}))
	defer srv.Close()
	defer close(release)
	c := New(srv.URL, srv.URL)
	c.HTTP.Timeout = 50 * time.Millisecond
	if _, err := c.Embed(t.Context(), []string{"a"}); err == nil {
		t.Fatal("no error from a server that never answers")
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("sent %d times, want 1", n)
	}
}

func TestServerErrorIsRetried(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		vectors(w, r)
	}))
	defer srv.Close()
	vecs, err := New(srv.URL, srv.URL).Embed(t.Context(), []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 3 || len(vecs) != 1 {
		t.Fatalf("hits %d vectors %d, want 3 and 1", n, len(vecs))
	}
}
