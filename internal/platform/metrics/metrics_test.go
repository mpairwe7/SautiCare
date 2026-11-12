package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrometheusExposition(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("sauti_outbox_relayed_total", "Outbox rows delivered to the audit ledger.")
	g := r.Gauge("sauti_outbox_lag_seconds", "Age of the oldest undelivered outbox row.")
	c.Add(2)
	c.Add(3)
	g.Set(1.5)
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		"# HELP sauti_outbox_relayed_total Outbox rows delivered to the audit ledger.",
		"# TYPE sauti_outbox_relayed_total counter",
		"sauti_outbox_relayed_total 5",
		"# TYPE sauti_outbox_lag_seconds gauge",
		"sauti_outbox_lag_seconds 1.5",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition missing %q:\n%s", want, body)
		}
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content type %q", ct)
	}
}

func TestDuplicateNamePanics(t *testing.T) {
	r := NewRegistry()
	r.Counter("x_total", "h")
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate metric name accepted")
		}
	}()
	r.Gauge("x_total", "h")
}

// CodeRabbit PR #2: registering a metric while /metrics is served must not
// race on the registry map (run with -race in CI).
func TestHandlerIsSafeDuringRegistration(t *testing.T) {
	r := NewRegistry()
	h := r.Handler()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			r.Counter("c_"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+strings.Repeat("y", i/26), "h")
		}
	}()
	for range 200 {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/metrics", nil))
	}
	<-done
}
