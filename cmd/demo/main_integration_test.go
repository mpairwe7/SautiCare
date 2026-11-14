//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// pgBin finds PostgreSQL 18 binaries: SAUTI_DEMO_PG_BIN, the embedded
// PostgreSQL that the integration tests cache, or $PATH. The test is
// skipped without them.
func pgBin(t *testing.T) string {
	t.Helper()
	var candidates []string
	if d := os.Getenv("SAUTI_DEMO_PG_BIN"); d != "" {
		candidates = append(candidates, d)
	}
	if cache, err := os.UserCacheDir(); err == nil {
		m, _ := filepath.Glob(filepath.Join(cache, "sauti", "embedded-postgres", "postgresql-18*", "*", "bin"))
		candidates = append(candidates, m...)
	}
	if p, err := exec.LookPath("initdb"); err == nil {
		candidates = append(candidates, filepath.Dir(p))
	}
	for _, d := range candidates {
		if checkPostgres(d) == nil {
			return d
		}
	}
	t.Skip("no PostgreSQL 18 binaries (set SAUTI_DEMO_PG_BIN)")
	return ""
}

type client struct {
	t    *testing.T
	base string
}

func (c client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, c.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw := new(bytes.Buffer)
	_, _ = raw.ReadFrom(resp.Body)
	if out != nil {
		if err := json.Unmarshal(raw.Bytes(), out); err != nil {
			c.t.Fatalf("%s %s: %v: %s", method, path, err, raw)
		}
	}
	return resp.StatusCode
}

func (c client) raw(path string) string {
	c.t.Helper()
	var v json.RawMessage
	c.do(http.MethodGet, path, nil, &v)
	return string(v)
}

func eventually(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

type screen struct {
	Session, Text string
	End           bool
}

// dialAndWalk dials code from a phone and sends each input, returning the
// last screen.
func (c client) dialAndWalk(phone, code string, inputs ...string) screen {
	c.t.Helper()
	var sc screen
	if st := c.do(http.MethodPost, "/api/ussd", map[string]string{"phone": phone, "code": code}, &sc); st != http.StatusOK {
		c.t.Fatalf("dial %s from %s: %d", code, phone, st)
	}
	for _, in := range inputs {
		if sc.End {
			c.t.Fatalf("session ended before %q: %q", in, sc.Text)
		}
		if st := c.do(http.MethodPost, "/api/ussd", map[string]string{"session": sc.Session, "input": in}, &sc); st != http.StatusOK {
			c.t.Fatalf("reply %q: %d", in, st)
		}
	}
	return sc
}

type boardView struct {
	Incidents []struct {
		Reference, Severity, State string
		Events                     []struct{ Type string }
		Alerts                     []struct {
			Responder, Outcome string
			Attempts           []struct{ Channel, Status string }
		}
	}
}

// runnerProc is the demo runner under test.
type runnerProc struct {
	cmd  *exec.Cmd
	logs lockedBuffer
	done chan struct{}
	err  error // the exit status, set before done closes
	addr string
	base string
}

// lockedBuffer collects the runner's output, which the test reads while the
// runner writes it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// buildDemo builds the runner and the three services into a temporary
// directory and returns it.
func buildDemo(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	build := exec.Command("go", "build", "-o", bin+"/", "./cmd/demo", "./cmd/intake-gw", "./cmd/dispatch-worker", "./cmd/outbox-relay")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

// pickPort picks the runner's UI port: a variable, so a test can hand the
// runner a port that another server holds.
var pickPort = func() (string, error) {
	ports, err := freePorts(1)
	if err != nil {
		return "", err
	}
	return ports[0], nil
}

// startRunner starts the runner on a free port and waits until it says it
// listens there. A port free when picked can be taken before the runner
// binds it; the runner then exits with "address already in use" and is
// started again on a new port, up to three times (CodeRabbit PR #15). At
// cleanup the runner gets SIGTERM, so it stops PostgreSQL and its services.
func startRunner(t *testing.T, path string, args ...string) *runnerProc {
	t.Helper()
	for attempt := 1; ; attempt++ {
		port, err := pickPort()
		if err != nil {
			t.Fatal(err)
		}
		r := &runnerProc{done: make(chan struct{}), addr: "127.0.0.1:" + port, base: "http://127.0.0.1:" + port}
		r.cmd = exec.Command(path, append(args, "--addr", r.addr)...)
		r.cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
		r.cmd.Stdout, r.cmd.Stderr = &r.logs, &r.logs
		if err := r.cmd.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { r.err = r.cmd.Wait(); close(r.done) }()
		if r.up() {
			t.Cleanup(func() {
				if !r.stop() {
					t.Error("the demo did not stop on SIGTERM")
				}
				if t.Failed() {
					t.Logf("runner log:\n%s", r.logs.String())
				}
			})
			return r
		}
		if attempt == 3 || !strings.Contains(r.logs.String(), "address already in use") {
			t.Fatalf("the runner did not come up (attempt %d): %v\n%s", attempt, r.err, r.logs.String())
		}
	}
}

// up waits for the runner's own word that it listens on its address, the
// line it logs only after the bind succeeded (true), or for it to exit or
// stay silent for 30 s (false). An answer on the port would prove nothing:
// another server may hold it.
func (r *runnerProc) up() bool {
	listening := "demo UI listening"
	at := "addr=" + r.addr + "\n"
	deadline := time.After(30 * time.Second)
	for {
		if out := r.logs.String(); strings.Contains(out, listening) && strings.Contains(out, at) {
			return true
		}
		select {
		case <-r.done:
			return false
		case <-deadline:
			r.stop()
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// stop sends SIGTERM and waits a minute for the runner to stop its
// children and exit; only then does it kill it. It reports whether the
// runner exited on SIGTERM (or had already exited).
func (r *runnerProc) stop() bool {
	_ = r.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-r.done:
		return true
	case <-time.After(time.Minute):
		_ = r.cmd.Process.Kill()
		<-r.done
		return false
	}
}

// CodeRabbit PR #15: an answer on the picked port proves nothing, since
// another server may hold it. startRunner waits for the runner's own word
// that it listens there, and retries on a new port when it could not bind.
func TestStartRunnerIgnoresAForeignServer(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("PostgreSQL refuses to run as root")
	}
	pg := pgBin(t)
	bin := buildDemo(t)
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"phase":"ready","run":1}`))
	}))
	defer foreign.Close()
	_, held, err := net.SplitHostPort(foreign.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	old, picks := pickPort, 0
	pickPort = func() (string, error) {
		if picks++; picks == 1 {
			return held, nil
		}
		return old()
	}
	t.Cleanup(func() { pickPort = old })
	r := startRunner(t, filepath.Join(bin, "demo"), "--pg-bin", pg, "--data", t.TempDir(), "--config-dir", "../../config", "--rules-root", "../..")
	if picks != 2 || r.base == foreign.URL {
		t.Fatalf("after %d picks the runner is at %s (the foreign server is %s)", picks, r.base, foreign.URL)
	}
}

// Scenario 1 through the demo, over HTTP, as a presenter would run it: the
// runner starts its own PostgreSQL and the three services; a caller reports
// a red trauma on USSD; the alert SMS reaches the responders' inboxes; one
// accepts on the responder menu; the board shows the incident with its
// events and the accepted alert; the ledger verifies; nothing shows the
// caller's number; Reset erases everything; SIGTERM stops it all cleanly.
func TestDemoScenario1OverHTTP(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("PostgreSQL refuses to run as root")
	}
	pg := pgBin(t)
	bin := buildDemo(t)
	data := t.TempDir()
	r := startRunner(t, filepath.Join(bin, "demo"), "--pg-bin", pg, "--data", data, "--config-dir", "../../config", "--rules-root", "../..")
	c := client{t: t, base: r.base}
	ready := func() bool { p, _ := demoPhase(t, c.base); return p == phaseReady }
	eventually(t, "the demo to start", 2*time.Minute, ready)

	// 1. The caller completes Tool 1: English, danger yes, injury, 18-59,
	// no, Test Ward 1, its second landmark, this number.
	done := c.dialAndWalk("caller-a", callerCode, "1", "1", "1", "3", "2", "1", "2", "1")
	ref := regexp.MustCompile(`[0-9]{3} [0-9]{3} [0-9]{4}`).FindString(done.Text)
	if !done.End || ref == "" {
		t.Fatalf("closing screen %+v", done)
	}

	// 2. The alert SMS reaches the ward's responders through the fake.
	var inbox map[string][]struct{ Text string }
	eventually(t, "the alert SMS", 30*time.Second, func() bool {
		c.do(http.MethodGet, "/api/sms", nil, &inbox)
		return len(inbox["vht-1"]) > 0 && strings.Contains(inbox["vht-1"][0].Text, ref)
	})

	// 3. VHT 1 accepts on the responder menu.
	menu := c.dialAndWalk("vht-1", responderCode)
	if !strings.Contains(menu.Text, ref) || !strings.Contains(menu.Text, "1 Accept") {
		t.Fatalf("responder menu %q", menu.Text)
	}
	var accepted screen
	c.do(http.MethodPost, "/api/ussd", map[string]string{"session": menu.Session, "input": "1"}, &accepted)
	if !accepted.End || !strings.Contains(accepted.Text, "Accepted") {
		t.Fatalf("accept: %+v", accepted)
	}

	// 4. The board shows the incident, its events and the accepted alert.
	eventually(t, "the board to show the acceptance", 30*time.Second, func() bool {
		var b boardView
		c.do(http.MethodGet, "/api/board", nil, &b)
		if len(b.Incidents) != 1 || b.Incidents[0].Reference != ref || b.Incidents[0].Severity != "red" {
			return false
		}
		in := b.Incidents[0]
		types := map[string]bool{}
		for _, e := range in.Events {
			types[e.Type] = true
		}
		acceptedBy := ""
		for _, a := range in.Alerts {
			if a.Outcome == "accepted" && len(a.Attempts) > 0 && a.Attempts[0].Channel == "sms" && a.Attempts[0].Status == "sent" {
				acceptedBy = a.Responder
			}
		}
		return in.State == "assigned" && acceptedBy == "VHT 1" && types["session.started"] && types["incident.classified"] &&
			types["incident.dispatch_ready"] && types["alert.issued"] && types["responder.accepted"]
	})
	eventually(t, "the caller's notice", 30*time.Second, func() bool {
		c.do(http.MethodGet, "/api/sms", nil, &inbox)
		return len(inbox["caller-a"]) > 0 && strings.Contains(inbox["caller-a"][0].Text, ref)
	})

	// 5. The audit ledger verifies once the relay has caught up, with the
	// head signed and witnessed (the relay may still append between the
	// signature and the verification, so wait for a run that saw no append).
	var audit auditResult
	eventually(t, "a passing verification of the whole ledger", 30*time.Second, func() bool {
		if st := c.do(http.MethodPost, "/api/audit", map[string]string{}, &audit); st != http.StatusOK {
			t.Fatalf("audit: %d", st)
		}
		if !audit.OK {
			t.Fatalf("audit failed: %+v", audit)
		}
		return audit.Pending == 0 && audit.Entries == audit.Events && audit.Entries > 0 && audit.CheckpointSeq == audit.Entries
	})
	if audit.WitnessesOK == 0 || audit.Drift != 0 {
		t.Fatalf("audit %+v", audit)
	}

	// The caller's number appears nowhere the UI reads.
	for _, path := range []string{"/api/board", "/api/state", "/api/sms"} {
		if body := c.raw(path); strings.Contains(body, "772123456") || strings.Contains(body, "772 123 456") {
			t.Fatalf("%s reveals the caller's number", path)
		}
	}

	// Reset erases everything and starts again.
	if st := c.do(http.MethodPost, "/api/reset", map[string]string{}, nil); st != http.StatusAccepted {
		t.Fatalf("reset: %d", st)
	}
	time.Sleep(200 * time.Millisecond)
	eventually(t, "the demo to come back", 2*time.Minute, ready)
	var b boardView
	c.do(http.MethodGet, "/api/board", nil, &b)
	c.do(http.MethodGet, "/api/sms", nil, &inbox)
	if len(b.Incidents) != 0 || len(inbox["vht-1"]) != 0 {
		t.Fatalf("after reset: %d incidents, %d messages", len(b.Incidents), len(inbox["vht-1"]))
	}

	// SIGTERM: a clean exit that erases the run's directory.
	if err := r.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.done:
		if r.err != nil {
			t.Fatalf("exit: %v", r.err)
		}
	case <-time.After(time.Minute):
		t.Fatal("the demo did not stop on SIGTERM")
	}
	left, _ := filepath.Glob(filepath.Join(data, "sauti-demo-*"))
	if len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}
