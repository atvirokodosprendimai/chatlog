// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/atvirokodosprendimai/chatlog/internal/search"
	"github.com/atvirokodosprendimai/chatlog/internal/store"
)

// A remote host reaches one chatlog through a socket that something forwards (ssh -R <remote.sock>:<local.sock>): to the
// client it is a unix socket like any other, and the relay below stands for the forwarding.
func TestTheClientWorksThroughAForwardedSocket(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "chatlog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dir, err := os.MkdirTemp("", "relay")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	real, forwarded := filepath.Join(dir, "real.sock"), filepath.Join(dir, "fwd.sock")
	ln, err := net.Listen("unix", real)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: Handler(&search.Service{Store: st}, func(context.Context) (Status, error) { return Status{}, nil })}
	go srv.Serve(ln)
	defer srv.Close()
	front, err := net.Listen("unix", forwarded)
	if err != nil {
		t.Fatal(err)
	}
	defer front.Close()
	go func() {
		for {
			in, err := front.Accept()
			if err != nil {
				return
			}
			go func() {
				out, err := net.Dial("unix", real)
				if err != nil {
					in.Close()
					return
				}
				go func() { io.Copy(out, in); out.Close() }()
				io.Copy(in, out)
				in.Close()
			}()
		}
	}()
	c := NewClient(forwarded)
	ctx := context.Background()
	if _, err := c.KVWrite(ctx, KVWriteRequest{Project: "webshop-fleet", Set: map[string]string{"now/x": "from the remote host", "global/rule/r": "for all"}}); err != nil {
		t.Fatal(err)
	}
	got, err := c.KVRead(ctx, KVReadRequest{Project: "webshop-fleet", Prefixes: []string{""}})
	if err != nil || len(got) != 2 || got[0].Value != "from the remote host" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := c.Status(ctx); err != nil {
		t.Fatalf("status through the relay: %v", err)
	}
}
