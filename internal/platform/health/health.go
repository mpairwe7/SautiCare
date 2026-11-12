// Package health serves liveness and readiness probes for HAProxy and
// systemd. Readiness lists only what is on the dispatch path for the
// process: analytics, payments or monitoring being down never makes intake
// or dispatch unready (Build Prompt rule 3.14; spec §7.6).
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

// Check reports nil when healthy.
type Check func(ctx context.Context) error

const checkTimeout = 2 * time.Second

func serve(checks map[string]Check) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		names := make([]string, 0, len(checks))
		for n := range checks {
			names = append(names, n)
		}
		sort.Strings(names)
		out := struct {
			Status string            `json:"status"`
			Checks map[string]string `json:"checks"`
		}{Status: "ok", Checks: map[string]string{}}
		for _, n := range names {
			ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
			err := checks[n](ctx)
			cancel()
			// Probe output carries no error detail (addresses, hosts).
			if err != nil {
				out.Checks[n], out.Status = "fail", "fail"
			} else {
				out.Checks[n] = "ok"
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if out.Status != "ok" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(out)
	}
}

// Handler serves GET /livez and /readyz.
func Handler(live, ready map[string]Check) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", serve(live))
	mux.HandleFunc("GET /readyz", serve(ready))
	return mux
}
