// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package api

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// The server exits when idle; a client whose call finds no socket starts it once and is answered.
func TestClientRevivesTheServer(t *testing.T) {
	dir, err := os.MkdirTemp("", "cl") // short: a unix socket path has a length limit
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s.sock")
	c := NewClient(sock)
	if _, err := c.Status(context.Background()); err == nil {
		t.Fatal("no server and no Revive: the call must fail")
	}
	revived := 0
	c.Revive = func() {
		revived++
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Error(err)
			return
		}
		t.Cleanup(func() { ln.Close() })
		go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"chunks":7}`)) }))
	}
	st, err := c.Status(context.Background())
	if err != nil || st.Chunks != 7 || revived != 1 {
		t.Fatalf("status %+v err %v revived %d", st, err, revived)
	}
	if _, err := c.Status(context.Background()); err != nil || revived != 1 {
		t.Fatalf("a live server must not be revived again: %v %d", err, revived)
	}
}
