package main

import (
	"bytes"
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
)

// The demo provisions server-global roles and databases: with
// SAUTI_ENV=production it is refused before any connection is opened
// (CodeRabbit PR #4).
func TestDemoRefusesProductionBeforeConnecting(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	t.Setenv("SAUTI_ENV", "production")
	t.Setenv("SAUTI_DEV_DB_PASSWORD", "x")
	var out, errOut bytes.Buffer
	code := Main([]string{"demo", "m0", "--admin-dsn", "postgres://postgres:x@" + ln.Addr().String() + "/postgres?sslmode=disable"}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "production") || accepted.Load() != 0 {
		t.Fatalf("exit=%d connections=%d err=%s; want a refusal before connecting", code, accepted.Load(), errOut.String())
	}
}
