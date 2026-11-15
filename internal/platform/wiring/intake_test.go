package wiring_test

import (
	"context"
	"crypto/mldsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/platform/wiring"
	"sautihealth.com/sauti/internal/triage"
)

// repoIntake returns the repository's configuration and triage bundle. No
// store: every refusal below comes before BuildIntake reads the gazetteer.
func repoIntake(t *testing.T) wiring.IntakeOptions {
	t.Helper()
	cfg, err := config.Load(os.DirFS("../../../config"))
	if err != nil {
		t.Fatal(err)
	}
	doc, sig, err := wiring.ReadTriageBundle("../../..", cfg.Triage.RulesFile)
	if err != nil {
		t.Fatal(err)
	}
	return wiring.IntakeOptions{Config: cfg, TriageDoc: doc, TriageSig: sig}
}

// The signature proves who signed a bundle; the approved digest decides
// which signed bundle runs (ADR-0043; evaluation audit V-29, ES-9). A bundle
// re-signed by a trusted development key after a one-byte edit to a comment
// is refused, in every environment.
func TestBuildIntakeRefusesAnUnapprovedBundle(t *testing.T) {
	o := repoIntake(t)
	sum := sha256.Sum256(o.TriageDoc)
	if o.Config.Triage.ApprovedSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("config/gated/triage.yaml approved_sha256 %q does not pin %s (sha256 %x)", o.Config.Triage.ApprovedSHA256, o.Config.Triage.RulesFile, sum)
	}
	sk, err := mldsa.GenerateKey(mldsa.MLDSA65())
	if err != nil {
		t.Fatal(err)
	}
	o.TriageDoc = []byte(strings.Replace(string(o.TriageDoc), "# Tool 1 severity rules", "# Tool 1 severity rules.", 1))
	if o.TriageSig, err = triage.Sign(sk, "dev-test", o.TriageDoc); err != nil {
		t.Fatal(err)
	}
	o.Config.Triage.TrustedKeys = map[string]string{"dev-test": base64.StdEncoding.EncodeToString(sk.PublicKey().Bytes())}
	for _, production := range []bool{false, true} {
		o.Production = production
		_, err := wiring.BuildIntake(context.Background(), o)
		if err == nil || !strings.Contains(err.Error(), "approved_sha256") {
			t.Errorf("production=%v: an unapproved signed bundle: err = %v, want a refusal naming approved_sha256", production, err)
		}
	}
}

// Production never runs a bundle that no digest pins.
func TestProductionRequiresAnApprovedDigest(t *testing.T) {
	o := repoIntake(t)
	o.Config.Triage.ApprovedSHA256 = ""
	o.Production = true
	if _, err := wiring.BuildIntake(context.Background(), o); err == nil || !strings.Contains(err.Error(), "approved_sha256") {
		t.Fatalf("production with no approved digest: err = %v, want a refusal naming approved_sha256", err)
	}
}
