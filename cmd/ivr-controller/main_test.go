package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"sautihealth.com/sauti/internal/platform/health"
)

type stream bool

func (s stream) EventsConnected() bool { return bool(s) }

// I4: the voice line is not ready while the ARI event stream is down (no
// call can be served; the dialplan's fallback takes them), even if the
// database is up.
func TestReadinessIncludesTheARIEventStream(t *testing.T) {
	dbUp := func(context.Context) error { return nil }
	for _, tc := range []struct {
		events bool
		code   int
		state  string
	}{{true, http.StatusOK, "ok"}, {false, http.StatusServiceUnavailable, "fail"}} {
		rec := httptest.NewRecorder()
		health.Handler(nil, readiness(dbUp, stream(tc.events))).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		var body struct {
			Checks map[string]string `json:"checks"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != tc.code || body.Checks["ari_events"] != tc.state || body.Checks["operational_db"] != "ok" {
			t.Fatalf("events up=%v: %d %v", tc.events, rec.Code, body.Checks)
		}
	}
}
