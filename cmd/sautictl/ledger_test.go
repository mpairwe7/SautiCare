package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unreachableDSN refuses connections at once: whatever a test asserts must
// happen before the first query.
const unreachableDSN = "postgres://x@127.0.0.1:1/x"

// devKeys sets SAUTI_ENV=test and writes development keys to a new directory.
func devKeys(t *testing.T) string {
	t.Helper()
	t.Setenv("SAUTI_ENV", "test")
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := Main([]string{"dev-keygen", "--dir", dir}, &out, &errOut); code != 0 {
		t.Fatalf("keygen: %s", errOut.String())
	}
	return dir
}

// An empty, unmounted or mistyped --witness-dir gives no witnesses, which
// is as blind to a database owner's rewrite as --no-witness. A nightly run
// must not report "ledger OK" then: it warns and fails before connecting,
// and only an explicit --no-witness accepts chain-only verification
// (CodeRabbit PR #4 and PR #5).
func TestVerifyLedgerFailsWhenTheWitnessDirHoldsNoNotes(t *testing.T) {
	keys := devKeys(t)
	for name, dir := range map[string]string{"empty": t.TempDir(), "missing": filepath.Join(t.TempDir(), "unmounted")} {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := Main([]string{"verify-ledger", "--dsn", unreachableDSN, "--devkeys-dir", keys, "--witness-dir", dir}, &out, &errOut)
			if !strings.Contains(out.String(), "WARNING: --witness-dir holds no checkpoints") ||
				!strings.Contains(out.String(), "rewritten-and-recomputed history or deleted checkpoints cannot be detected") {
				t.Fatalf("no warning for a witness directory without notes; out=%q err=%q", out.String(), errOut.String())
			}
			if code != 1 || !strings.Contains(errOut.String(), "--witness-dir holds no checkpoints") ||
				!strings.Contains(errOut.String(), "--no-witness") {
				t.Fatalf("an empty witness must fail verification before connecting; exit=%d err=%q", code, errOut.String())
			}
		})
	}
}

// --no-witness is the only way to accept chain-only verification. Combined
// with --witness-dir it is ambiguous, and a standing job given both would
// silently degrade if the witness volume went missing: refuse it.
func TestVerifyLedgerRejectsWitnessDirWithNoWitness(t *testing.T) {
	keys := devKeys(t)
	var out, errOut bytes.Buffer
	code := Main([]string{"verify-ledger", "--dsn", unreachableDSN, "--devkeys-dir", keys, "--witness-dir", t.TempDir(), "--no-witness"}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "mutually exclusive") {
		t.Fatalf("exit=%d err=%q", code, errOut.String())
	}
}

// The checkpoint command anchors on the witness before it signs, so a
// witness store that does not verify stops it (CodeRabbit PR #4).
func TestCheckpointRefusesAWitnessThatDoesNotVerify(t *testing.T) {
	keys := devKeys(t)
	witness := t.TempDir()
	if err := os.WriteFile(filepath.Join(witness, "checkpoint-3-1.txt"), []byte("not a note\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Main([]string{"checkpoint", "--dsn", unreachableDSN, "--devkeys-dir", keys, "--witness-dir", witness}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "witness store failed verification") {
		t.Fatalf("exit=%d err=%s", code, errOut.String())
	}
	errOut.Reset()
	code = Main([]string{"checkpoint", "--dsn", unreachableDSN, "--devkeys-dir", keys}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "--witness-dir is required") {
		t.Fatalf("without --witness-dir: exit=%d err=%s", code, errOut.String())
	}
}
