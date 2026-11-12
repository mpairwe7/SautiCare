package main

import (
	"bytes"
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
