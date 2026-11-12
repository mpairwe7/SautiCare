//go:build integration

// Package e2e holds end-to-end scenarios that run the real processes'
// components against PostgreSQL 18 and the simulators (Build Prompt §16).
package e2e

import (
	"context"
	"strings"
	"testing"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/demo"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

type logWriter struct{ t *testing.T }

func (w logWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// The M0 milestone demo (Build Prompt §18; spec §12.2): a fixture incident
// flows from a simulated dial through the event log into the hash-chained
// audit ledger, a checkpoint is signed and witnessed, verification passes,
// tampering is detected, and a retried callback changes nothing.
func TestM0FixtureIncident(t *testing.T) {
	rep, err := demo.RunM0(context.Background(), demo.Options{
		AdminDSN: pgtest.AdminURL(t), ConfigDir: "../../config", Out: logWriter{t},
	})
	if err != nil {
		t.Fatalf("M0 demo failed: %v", err)
	}
	if _, err := incident.ParseReference(rep.Reference); err != nil {
		t.Errorf("step 1: reference %q is not Damm-valid: %v", rep.Reference, err)
	}
	if rep.T1.IsZero() {
		t.Error("step 1: T1 not set by the database")
	}
	if rep.Events < 10 || rep.LedgerEntries != int64(rep.Events) {
		t.Errorf("steps 2-4: events=%d ledger=%d, want equal and >= 10", rep.Events, rep.LedgerEntries)
	}
	if rep.PIIInLedger {
		t.Error("step 4: a phone number reached the audit ledger")
	}
	if rep.CheckpointSeq != rep.LedgerEntries || !rep.WitnessWritten {
		t.Errorf("step 5: checkpoint seq=%d witness=%v", rep.CheckpointSeq, rep.WitnessWritten)
	}
	if !rep.LedgerVerified || rep.ProjectionsChecked != 1 || rep.ProjectionDrift != 0 {
		t.Errorf("step 6: ledger verified=%v projections checked=%d drift=%d", rep.LedgerVerified, rep.ProjectionsChecked, rep.ProjectionDrift)
	}
	if rep.TamperDetectedAt != 3 {
		t.Errorf("step 7: tamper detected at seq %d, want 3", rep.TamperDetectedAt)
	}
	if !rep.WitnessCaught || !rep.CheckpointRefused {
		t.Errorf("step 7b: rewrite with recomputed chain: witness caught=%v, checkpoint refused=%v", rep.WitnessCaught, rep.CheckpointRefused)
	}
	if !rep.RetryIdentical {
		t.Error("step 8: retried callback did not return the identical screen without a new event")
	}
}
