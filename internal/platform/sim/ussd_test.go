package sim

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"sautihealth.com/sauti/internal/platform/egress"
)

func TestUSSDSimAccumulatesText(t *testing.T) {
	var mu sync.Mutex
	var forms []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		forms = append(forms, r.PostForm)
		mu.Unlock()
		if !strings.HasSuffix(r.URL.Path, "/tok-0123456789abcdefghijkl") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("CON next"))
	}))
	defer srv.Close()

	s := NewUSSD(srv.URL, "tok-0123456789abcdefghijkl", "*384*1#", "64110").Dial("+256772123456")
	for _, in := range []string{"", "1", "2"} {
		sc, err := s.Send(in)
		if err != nil || sc.End || sc.Text != "next" {
			t.Fatalf("Send(%q) = %+v, %v", in, sc, err)
		}
	}
	if _, err := s.Retry(); err != nil {
		t.Fatal(err)
	}
	texts := []string{forms[0].Get("text"), forms[1].Get("text"), forms[2].Get("text"), forms[3].Get("text")}
	if texts[0] != "" || texts[1] != "1" || texts[2] != "1*2" || texts[3] != "1*2" {
		t.Fatalf("texts = %q", texts)
	}
	if forms[0].Get("sessionId") == "" || forms[0].Get("sessionId") != forms[3].Get("sessionId") {
		t.Fatal("session id not stable across the session")
	}
	if forms[0].Get("phoneNumber") != "+256772123456" || forms[0].Get("networkCode") != "64110" || forms[0].Get("serviceCode") != "*384*1#" {
		t.Fatalf("form = %v", forms[0])
	}
}

func TestUSSDSimParsesEndAndRejectsGarbage(t *testing.T) {
	body := "END Bye"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer srv.Close()
	s := NewUSSD(srv.URL, "t", "*1#", "64101").Dial("+256700000001")
	if sc, err := s.Send(""); err != nil || !sc.End || sc.Text != "Bye" {
		t.Fatalf("END parse = %+v, %v", sc, err)
	}
	body = "<html>oops</html>"
	if _, err := s.Send("1"); err == nil {
		t.Fatal("non-USSD body accepted")
	}
}

// The simulator's outbound HTTP goes through platform/egress, allowing only
// the gateway host from its base URL: a redirect to any other host is
// refused before a connection is made (CodeRabbit PR #4).
func TestUSSDSimReachesOnlyTheGatewayHost(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("no second loopback address: %v", err)
	}
	var reached atomic.Bool
	elsewhere := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		_, _ = w.Write([]byte("END elsewhere"))
	}))
	_ = elsewhere.Listener.Close()
	elsewhere.Listener = ln
	elsewhere.Start()
	defer elsewhere.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer gateway.Close()

	_, err = NewUSSD(gateway.URL, "t", "*1#", "64101").Dial("+256700000001").Send("")
	if !errors.Is(err, egress.ErrDenied) || reached.Load() {
		t.Fatalf("redirect off the gateway host: err = %v, reached = %v; want egress.ErrDenied", err, reached.Load())
	}
}
