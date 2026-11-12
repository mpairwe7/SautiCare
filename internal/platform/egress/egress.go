// Package egress is the only way Sauti code makes outbound HTTP requests.
// Every request's host is checked against an explicit allowlist before a
// connection is attempted (hard constraint 1; spec §7.8). In production
// nftables default-deny enforces the same rule a second time.
package egress

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ErrDenied reports a request to a host outside the allowlist.
var ErrDenied = errors.New("egress: host not in allowlist")

type guard struct {
	allow map[string]bool
	next  http.RoundTripper
}

func (g *guard) RoundTrip(req *http.Request) (*http.Response, error) {
	host := strings.ToLower(req.URL.Hostname())
	if !g.allow[host] {
		return nil, fmt.Errorf("%w: %s", ErrDenied, host)
	}
	return g.next.RoundTrip(req)
}

// NewClient returns an *http.Client that refuses every host not in allow.
// Redirects are re-checked hop by hop because each hop passes through the
// guarded transport.
// DefaultTimeout applies when a caller passes a non-positive timeout: every
// external call has a deadline (spec §7.5).
const DefaultTimeout = 10 * time.Second

func NewClient(allow []string, timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	set := make(map[string]bool, len(allow))
	for _, h := range allow {
		set[strings.ToLower(strings.TrimSpace(h))] = true
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil // a proxy would hide the real destination from the guard
	return &http.Client{
		Timeout:   timeout,
		Transport: &guard{allow: set, next: base},
	}
}
