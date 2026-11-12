package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestUnknownCommandPrintsUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Main([]string{"nope"}, &out, &errOut); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "verify-ledger") {
		t.Fatalf("usage missing commands: %s", errOut.String())
	}
}

func TestKeygenRefusesProduction(t *testing.T) {
	t.Setenv("SAUTI_ENV", "production")
	var out, errOut bytes.Buffer
	if code := Main([]string{"dev-keygen", "--dir", t.TempDir()}, &out, &errOut); code != 1 {
		t.Fatalf("exit = %d, want 1 (%s)", code, errOut.String())
	}
}

func TestDemoNeedsAServer(t *testing.T) {
	t.Setenv("SAUTI_TEST_PG_DSN", "")
	var out, errOut bytes.Buffer
	if code := Main([]string{"demo", "m0"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "admin-dsn") {
		t.Fatalf("exit = %d, err = %s", code, errOut.String())
	}
}

// Without the write-once witness, a database owner's rewrite is undetectable
// (review finding C1): verify-ledger must demand it or an explicit opt-out.
func TestVerifyLedgerRequiresWitnessOrExplicitOptOut(t *testing.T) {
	t.Setenv("SAUTI_ENV", "test")
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := Main([]string{"dev-keygen", "--dir", dir}, &out, &errOut); code != 0 {
		t.Fatalf("keygen: %s", errOut.String())
	}
	errOut.Reset()
	code := Main([]string{"verify-ledger", "--dsn", "postgres://x@127.0.0.1:1/x", "--devkeys-dir", dir}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "--witness-dir is required") {
		t.Fatalf("exit=%d err=%s", code, errOut.String())
	}
}

// A malformed gazetteer is refused before any database connection, so a
// typo can never reach the store.
func TestGazetteerLoadValidatesBeforeConnecting(t *testing.T) {
	bad := t.TempDir() + "/g.yaml"
	if err := os.WriteFile(bad, []byte("version: v\nstatus: draft\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Main([]string{"gazetteer", "load", "--file", bad, "--dsn", "postgres://invalid.invalid/x"}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "status") {
		t.Fatalf("exit = %d, err = %s", code, errOut.String())
	}
	if code := Main([]string{"gazetteer", "load", "--file", "../../config/gated/gazetteer.yaml"}, &out, &errOut); code != 1 ||
		!strings.Contains(errOut.String(), "--dsn") {
		t.Fatalf("missing --dsn: exit = %d, err = %s", code, errOut.String())
	}
}
