//go:build integration

package main

import (
	"bytes"
	"strings"
	"testing"

	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

// A coordinator suspends and resumes dispatch from the command line; status
// shows the state and the dated register.
func TestDispatchSuspendStatusResume(t *testing.T) {
	t.Setenv("SAUTI_ENV", "dev")
	c := pgtest.Cluster(t)
	console, ro := c.DSN(pg.StoreOperational, "op_console"), c.DSN(pg.StoreOperational, "op_readonly")
	staff := "0195f3a2-7c1e-7d4b-9a10-2b3c4d5e6f70"
	run := func(args ...string) string {
		t.Helper()
		var out, errOut bytes.Buffer
		if code := Main(args, &out, &errOut); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, errOut.String())
		}
		return out.String()
	}
	if s := run("dispatch", "status", "--dsn", ro); !strings.Contains(s, "dispatch: active") || !strings.Contains(s, "surge: no (0 waiting, 0 free responders)") {
		t.Fatalf("status: %s", s)
	}
	run("dispatch", "suspend", "--dsn", console, "--config", "../../config", "--reason", "coordinator_decision", "--staff", staff)
	if s := run("dispatch", "status", "--dsn", ro); !strings.Contains(s, "dispatch: SUSPENDED") || !strings.Contains(s, "suspended coordinator_decision") {
		t.Fatalf("status after suspend: %s", s)
	}
	run("dispatch", "resume", "--dsn", console, "--reason", "reviewed", "--staff", staff)
	if s := run("dispatch", "status", "--dsn", ro); !strings.Contains(s, "dispatch: active") || !strings.Contains(s, "resumed reviewed") {
		t.Fatalf("status after resume: %s", s)
	}
}

// CodeRabbit PR #10 (4557205821): switching a responder off reports the off
// state, not an availability expiry (the database returns an empty period).
// Registration needs a stage of the gazetteer in force.
func TestResponderAvailableOnAndOff(t *testing.T) {
	keys := devKeys(t)
	t.Setenv("SAUTI_ENV", "dev")
	c := pgtest.Cluster(t)
	console := c.DSN(pg.StoreOperational, "op_console")
	run := func(args ...string) string {
		t.Helper()
		var out, errOut bytes.Buffer
		if code := Main(args, &out, &errOut); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, errOut.String())
		}
		return out.String()
	}
	run("gazetteer", "load", "--file", "../../config/gated/gazetteer.yaml",
		"--dsn", c.DSN(pg.StoreOperational, pg.StoreOperational.Owner()))
	reg := strings.Fields(run("responder", "add", "--dsn", console, "--devkeys-dir", keys, "--role", "vht",
		"--name", "Synthetic", "--phone", "0772000901", "--ward", "w01", "--stage", "w01_stage_a"))
	id := reg[len(reg)-1]
	if s := run("responder", "available", "--dsn", console, "--id", id); !strings.Contains(s, "available until") {
		t.Fatalf("on: %s", s)
	}
	if s := run("responder", "available", "--dsn", console, "--id", id, "--off"); strings.Contains(s, "until") ||
		!strings.Contains(s, "switched off") {
		t.Fatalf("off: %s", s)
	}
}
