package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code, rec.Body.String()
}

func TestReadyFailsWhenACheckFails(t *testing.T) {
	h := Handler(nil, map[string]Check{
		"operational_db": func(context.Context) error { return errors.New("dial tcp 10.0.0.5: refused") },
	})
	code, body := get(t, h, "/readyz")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, `"operational_db":"fail"`) {
		t.Fatalf("readyz = %d %s", code, body)
	}
	// Internal error detail (addresses) must not be exposed on the probe.
	if strings.Contains(body, "10.0.0.5") {
		t.Fatalf("probe leaked error detail: %s", body)
	}
}

func TestLiveAndReadyOK(t *testing.T) {
	ok := func(context.Context) error { return nil }
	h := Handler(map[string]Check{"loop": ok}, map[string]Check{"operational_db": ok})
	for _, p := range []string{"/livez", "/readyz"} {
		if code, body := get(t, h, p); code != http.StatusOK || !strings.Contains(body, `"status":"ok"`) {
			t.Fatalf("%s = %d %s", p, code, body)
		}
	}
}
