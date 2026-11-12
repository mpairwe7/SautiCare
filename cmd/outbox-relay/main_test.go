package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sautihealth.com/sauti/internal/audit"
	"sautihealth.com/sauti/internal/platform/metrics"
)

func scrape(t *testing.T, reg *metrics.Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

// Once an operator restores the witnessed history and a checkpoint signs
// again, the divergence alarm clears (CodeRabbit PR #4); an unrelated
// failure leaves it as it was.
func TestSuccessfulCheckpointClearsTheDivergenceGauge(t *testing.T) {
	reg := metrics.NewRegistry()
	m := newRelayMetrics(reg)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	m.checkpointed(log, audit.Checkpoint{}, fmt.Errorf("checkpoint: %w", audit.ErrChainDiverged))
	if got := scrape(t, reg); !strings.Contains(got, "\nsauti_audit_chain_diverged 1\n") {
		t.Fatalf("after divergence:\n%s", got)
	}
	m.checkpointed(log, audit.Checkpoint{}, errors.New("checkpoint: head: connection refused"))
	if got := scrape(t, reg); !strings.Contains(got, "\nsauti_audit_chain_diverged 1\n") {
		t.Fatalf("an I/O failure cleared the divergence alarm:\n%s", got)
	}
	m.checkpointed(log, audit.Checkpoint{Seq: 9}, nil)
	got := scrape(t, reg)
	if !strings.Contains(got, "\nsauti_audit_chain_diverged 0\n") || !strings.Contains(got, "\nsauti_audit_checkpoints_total 1\n") {
		t.Fatalf("after a successful checkpoint:\n%s", got)
	}
}
