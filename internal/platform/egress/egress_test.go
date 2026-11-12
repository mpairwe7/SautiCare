package egress

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// Hard constraint 1: no outbound request may reach a host outside the
// allowlist. The check must happen before any connection is made.
func TestEgressDeniesUnlistedHost(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()

	c := NewClient([]string{"api.example.ug"}, 2*time.Second)
	_, err := c.Get(srv.URL)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
	if hit {
		t.Fatal("request reached the server despite denial")
	}
}

func TestEgressAllowsListedHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	c := NewClient([]string{u.Hostname()}, 2*time.Second)
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("allowed host denied: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// Redirects are a classic allowlist bypass: an allowed host must not be able
// to bounce the client to a disallowed one.
func TestEgressDeniesRedirectToUnlistedHost(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer other.Close()
	otherURL, _ := url.Parse(other.URL)
	otherURL.Host = "localhost:" + otherURL.Port() // different hostname, same listener

	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, otherURL.String(), http.StatusFound)
	}))
	defer front.Close()
	fu, _ := url.Parse(front.URL)

	c := NewClient([]string{fu.Hostname()}, 2*time.Second)
	_, err := c.Get(front.URL)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("redirect err = %v, want ErrDenied", err)
	}
}

// CodeRabbit PR #2: every external call has a deadline; a zero timeout
// must not mean "wait forever".
func TestNewClientAlwaysHasATimeout(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		if c := NewClient([]string{"x"}, d); c.Timeout <= 0 {
			t.Fatalf("NewClient(timeout=%v).Timeout = %v, want a positive default", d, c.Timeout)
		}
	}
}
