package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"sautihealth.com/sauti/internal/triage"
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

// keygen then sign produces a signature that the triage loader accepts under
// the printed key; keygen never overwrites an existing key file.
func TestTriageKeygenAndSign(t *testing.T) {
	t.Setenv("SAUTI_ENV", "dev")
	dir := t.TempDir()
	keyFile := dir + "/triage.key"
	var out, errOut bytes.Buffer
	if code := Main([]string{"triage", "keygen", "--out", keyFile, "--key-id", "dev-triage-test"}, &out, &errOut); code != 0 {
		t.Fatalf("keygen: %d %s", code, errOut.String())
	}
	pub := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out.String()), "dev-triage-test:"))
	if code := Main([]string{"triage", "keygen", "--out", keyFile, "--key-id", "dev-triage-test"}, &out, &errOut); code != 1 {
		t.Fatal("keygen overwrote an existing key")
	}
	rules, err := os.ReadFile("../../internal/triage/testdata/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	bundle := dir + "/rules.yaml"
	if err := os.WriteFile(bundle, rules, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Main([]string{"triage", "sign", "--key", keyFile, "--key-id", "dev-triage-test", "--rules", bundle}, &out, &errOut); code != 0 {
		t.Fatalf("sign: %d %s", code, errOut.String())
	}
	sig, err := os.ReadFile(bundle + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	keys, err := triage.DecodeTrustedKeys(map[string]string{"dev-triage-test": pub})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := triage.Load(rules, sig, keys); err != nil {
		t.Fatalf("signed bundle does not load: %v", err)
	}
}

func TestTriageKeygenRefusesProduction(t *testing.T) {
	t.Setenv("SAUTI_ENV", "production")
	var out, errOut bytes.Buffer
	if code := Main([]string{"triage", "keygen", "--out", t.TempDir() + "/k", "--key-id", "dev-x"}, &out, &errOut); code != 1 {
		t.Fatalf("exit = %d", code)
	}
}

// A split names the survivor, the member and the coordinator's staff
// identity; anything malformed is refused before any connection.
func TestIncidentSplitValidatesBeforeConnecting(t *testing.T) {
	good := "0195f3a2-7c1e-7d4b-9a10-2b3c4d5e6f70"
	for _, args := range [][]string{
		{"incident", "split", "--dsn", "postgres://invalid.invalid/x", "--survivor", "x", "--member", good, "--staff", good},
		{"incident", "split", "--dsn", "postgres://invalid.invalid/x", "--survivor", good, "--member", good, "--staff", good},
		{"incident", "split", "--dsn", "postgres://invalid.invalid/x", "--survivor", good, "--member", "0195f3a2-7c1e-7d4b-9a10-2b3c4d5e6f71"},
		{"incident", "split", "--survivor", good, "--member", "0195f3a2-7c1e-7d4b-9a10-2b3c4d5e6f71", "--staff", good},
	} {
		var out, errOut bytes.Buffer
		if code := Main(args, &out, &errOut); code != 1 || strings.Contains(errOut.String(), "invalid.invalid") {
			t.Errorf("%v: exit %d, %s", args, code, errOut.String())
		}
	}
}
