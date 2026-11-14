package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/africastalking"
	"sautihealth.com/sauti/internal/platform/egress"
)

// ADR-0039: the public demo never runs in production and never reaches a
// real provider or store. It refuses before it starts anything.
func TestRefusesProductionAndRealProviders(t *testing.T) {
	for _, bad := range []string{
		"SAUTI_ENV=production", "SAUTI_ENV=prod",
		"SAUTI_AT_API_KEY=k", "SAUTI_AT_SMS_URL=https://api.africastalking.com", "SAUTI_AT_USERNAME=sauti",
		"SAUTI_ARI_URL=http://10.0.0.5:8088", "SAUTI_ARI_PASSWORD=p",
		"SAUTI_OP_DSN=postgres://op_intake@db/sauti_operational", "SAUTI_AUDIT_DSN=postgres://x@db/sauti_audit",
	} {
		environ := []string{"PATH=/usr/bin", bad}
		if err := checkEnvironment(environ); err == nil {
			t.Errorf("%s: accepted", bad)
		}
		// run refuses before it parses flags, listens or starts PostgreSQL.
		if err := run(slog.New(slog.NewTextHandler(io.Discard, nil)), []string{"--pg-bin", "/nonexistent"}, environ); err == nil ||
			!strings.Contains(err.Error(), "refusing") {
			t.Errorf("%s: run = %v, want a refusal", bad, err)
		}
	}
	for _, ok := range [][]string{{"PATH=/usr/bin"}, {"SAUTI_ENV=dev"}, {"SAUTI_ENV=test"}, {"SAUTI_ENV="}, {"SAUTI_TEST_PG_DSN=postgres://x@h/db"}} {
		if err := checkEnvironment(ok); err != nil {
			t.Errorf("%v: %v", ok, err)
		}
	}
	// The SMS adapter may point only at a loopback fake.
	for _, u := range []string{"https://api.africastalking.com", "https://api.sandbox.africastalking.com", "http://10.1.2.3:8080",
		"http://localhost:8080", "http://u:p@127.0.0.1:1", "ftp://127.0.0.1/"} {
		if checkLoopback(u) == nil {
			t.Errorf("provider endpoint %s accepted", u)
		}
	}
	if err := checkLoopback("http://127.0.0.1:41234"); err != nil {
		t.Fatal(err)
	}
	// Children get a fresh environment: nothing of the demo's own leaks in.
	t.Setenv("SAUTI_AT_API_KEY", "a-real-key")
	env := childEnv(map[string]string{"SAUTI_ENV": "dev"})
	if slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "SAUTI_AT_API_KEY=") }) ||
		!slices.Contains(env, "SAUTI_ENV=dev") {
		t.Fatalf("child environment %v", env)
	}
}

// A reply holding a phone number other than the demo's own synthetic ones is
// refused: a public page must never take a real person's number.
func TestRefusesRealLookingNumbers(t *testing.T) {
	phones := []phone{{e164: "+256772123456"}, {e164: "+256772000201"}}
	for _, in := range []string{"1", "6", "Test Market", "Mkt*near taxi", "123456", "", "+256772123456", "0772 123 456", "0772000201"} {
		if !syntheticOnly(in, phones) {
			t.Errorf("%q refused", in)
		}
	}
	for _, in := range []string{"0772999888", "+256 701 234 567", "256701234567", "+447700900123", "12345678", "call 0701234567"} {
		if syntheticOnly(in, phones) {
			t.Errorf("%q accepted", in)
		}
	}
}

// The board masks any phone number before display, however written, and
// leaves references, UUIDs and coded values alone (rule 5).
func TestBoardMasksNumbers(t *testing.T) {
	in := `{"a":"+256772123456","b":"0772123456","c":"256772123456","d":"+256 772 123 456","e":"+447700900123",` +
		`"reference":"3410613357","alert":"01a1d856-9bc6-7302-807f-f2e012345678","zone":{"col":900,"row":83}}`
	out := maskNumbers(in)
	for _, leak := range []string{"772123456", "772 123 456", "7700900123"} {
		if strings.Contains(out, leak) {
			t.Fatalf("%s leaks %s", out, leak)
		}
	}
	for _, keep := range []string{"3410613357", "01a1d856-9bc6-7302-807f-f2e012345678", `"col":900`} {
		if !strings.Contains(out, keep) {
			t.Fatalf("%s lost %s", out, keep)
		}
	}
}

// The fake SMS endpoint speaks the contract of the real Africa's Talking
// adapter, which dispatch-worker points at it: a message is accepted, kept
// for the recipient's phone and followed by a delivery report; a wrong key
// is refused as the provider refuses it.
func TestSinkSpeaksTheProviderContract(t *testing.T) {
	reported := make(chan string, 1)
	sink := &smsSink{apiKey: "demo-key", report: func(id string) { reported <- id }}
	srv := httptest.NewServer(sink)
	defer srv.Close()
	client := func(key string) *africastalking.SMSClient {
		c, err := africastalking.NewSMSClient(africastalking.SMSConfig{BaseURL: srv.URL, Username: "sauti-demo", APIKey: key,
			HTTP: egress.NewClient([]string{"127.0.0.1"}, 2*time.Second)})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	to, _ := pii.ParsePhone("+256772000201")
	id, err := client("demo-key").Send(context.Background(), to, "PENDING-SMS Sauti 341 061 3357: RED")
	if err != nil || id == "" {
		t.Fatalf("send = %q, %v", id, err)
	}
	if got := sink.inbox("+256772000201"); len(got) != 1 || got[0].Text != "PENDING-SMS Sauti 341 061 3357: RED" {
		t.Fatalf("inbox = %+v", got)
	}
	if other := sink.inbox("+256772000202"); len(other) != 0 {
		t.Fatalf("another phone's inbox = %+v", other)
	}
	select {
	case r := <-reported:
		if r != id {
			t.Fatalf("delivery report for %q, sent %q", r, id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no delivery report")
	}
	if _, err := client("wrong-key").Send(context.Background(), to, "x"); !errors.Is(err, dispatch.ErrRejected) {
		t.Fatalf("wrong key: %v, want a rejection", err)
	}
}

// The confidential governing documents (repository-root PDFs and sa/) never
// enter a Docker build context, even one sent from a working tree that holds
// them (ADR-0008, ADR-0039).
func TestDockerignoreKeepsConfidentialFilesOut(t *testing.T) {
	raw, err := os.ReadFile("../../.dockerignore")
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string]bool{}
	for _, l := range strings.Split(string(raw), "\n") {
		lines[strings.TrimSpace(l)] = true
	}
	for _, want := range []string{"*.pdf", "**/*.pdf", "sa/", ".git", ".devkeys", "var/", ".superpowers/", ".env*", "!.env.example", "bin/"} {
		if !lines[want] {
			t.Errorf(".dockerignore lacks %q", want)
		}
	}
}

// scripts/deploy/hf-space-stage.sh stages the committed tree for the Space:
// the Space card as README.md, the Dockerfile, and no confidential document.
func TestStagedSpaceHoldsNoConfidentialFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if err := exec.Command("git", "-C", "../..", "cat-file", "-e", "HEAD:scripts/deploy/hf-space-stage.sh").Run(); err != nil {
		t.Skip("the staging script is not committed yet")
	}
	out := filepath.Join(t.TempDir(), "space")
	cmd := exec.Command("bash", "../../scripts/deploy/hf-space-stage.sh", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stage: %v\n%s", err, b)
	}
	card, err := os.ReadFile(filepath.Join(out, "README.md"))
	if err != nil || !strings.HasPrefix(string(card), "---\n") || !strings.Contains(string(card), "sdk: docker") ||
		!strings.Contains(string(card), "app_port: 7860") {
		t.Fatalf("README.md is not the Space card: %v\n%s", err, card)
	}
	for _, f := range []string{"Dockerfile", ".dockerignore", "go.mod", "cmd/demo/main.go", "config/gated/gazetteer.yaml", "triage-rules/tool1.yaml.sig"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("staged tree lacks %s", f)
		}
	}
	_ = filepath.WalkDir(out, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.EqualFold(filepath.Ext(p), ".pdf") || (d.IsDir() && d.Name() == "sa") {
			t.Errorf("staged %s", p)
		}
		return nil
	})
}
