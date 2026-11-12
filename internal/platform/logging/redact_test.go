package logging

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func newLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(NewHandler(buf, slog.LevelDebug))
}

// Driver and library errors can echo bound parameters; the handler must
// scrub number-like sequences inside error values before they are written.
func TestRedactionMasksPhoneInsideErrorValues(t *testing.T) {
	var buf bytes.Buffer
	log := newLogger(&buf)
	log.Error("insert failed", "err", fmt.Errorf("pgx: value 0772123456 violates check"))
	log.Error("wrapped", "err", fmt.Errorf("outer: %w", errors.New("dial +256 772 123 456 refused")))
	log.Info("free text", "msg2", "caller said 256772123456 twice")
	out := buf.String()
	for _, leak := range []string{"772123456", "772 123 456"} {
		if strings.Contains(out, leak) {
			t.Fatalf("log leaked %q:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "[redacted-number]") {
		t.Fatalf("expected redaction marker in:\n%s", out)
	}
}

func TestRedactionMasksDeniedKeys(t *testing.T) {
	var buf bytes.Buffer
	log := newLogger(&buf)
	log.Info("x", "phone", "abc", "msisdn", "def", "phoneNumber", "ghi", "caller", "jkl", "name", "Nakato")
	out := buf.String()
	for _, leak := range []string{"abc", "def", "ghi", "jkl", "Nakato"} {
		if strings.Contains(out, leak) {
			t.Fatalf("denied key value %q leaked:\n%s", leak, out)
		}
	}
}

// The handler must not damage ordinary attributes: services log incident
// UUIDs (never phone numbers or the 10-digit reference) for correlation.
func TestRedactionKeepsNonNumericAttrs(t *testing.T) {
	var buf bytes.Buffer
	log := newLogger(&buf)
	log.Info("relay", "incident_id", "0195f3a2-7c1e-7d4b-9a10-2b3c4d5e6f70", "count", 42)
	out := buf.String()
	if !strings.Contains(out, "0195f3a2-7c1e-7d4b-9a10-2b3c4d5e6f70") || !strings.Contains(out, `"count":42`) {
		t.Fatalf("handler damaged ordinary attributes:\n%s", out)
	}
}

// CodeRabbit PR #2: an identifier logged as an integral float, or an email
// address, must not reach the output.
func TestRedactionCoversFloatsAndEmail(t *testing.T) {
	var buf bytes.Buffer
	log := newLogger(&buf)
	log.Info("x", "msisdn_as_float", 256772123456.0, "ratio", 0.25, "email", "alice@example.org")
	log.Info("inline", "detail", "contact alice@example.org now")
	out := buf.String()
	for _, leak := range []string{"256772123456", "2.56772123456e+11", "alice@example.org"} {
		if strings.Contains(out, leak) {
			t.Fatalf("leaked %q:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, `"ratio":0.25`) {
		t.Fatalf("ordinary float damaged:\n%s", out)
	}
}
