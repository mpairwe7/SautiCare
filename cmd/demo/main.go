// Command demo runs Sauti Dispatch in one process tree for an MVP
// presentation (ADR-0039; runbook docs/runbooks/demo-hf-space.md). It starts
// a private PostgreSQL 18 on loopback, provisions the five stores, keys and
// gazetteer (internal/platform/demo), registers synthetic responders through
// the registration path, launches intake-gw, dispatch-worker and outbox-relay
// as child processes, and serves a web UI: a basic-phone emulator for the
// caller, the responders' phones, the dispatch board and the ledger verifier.
//
// Synthetic data only, everything erased on restart and on Reset. SMS goes to
// a local fake of Africa's Talking; voice is not wired. The demo refuses
// SAUTI_ENV=production and any provider credential, endpoint or store DSN in
// its environment, and gives its child processes a fresh environment.
//
//	demo --pg-bin DIR [--data DIR] [--addr :7860] [--bin-dir DIR] [--config-dir config] [--rules-root .]
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/location"
)

// services are the commands the demo launches, in start order.
var services = []string{"intake-gw", "dispatch-worker", "outbox-relay"}

type options struct {
	pgBin, data, addr, binDir, configDir, rulesRoot string
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log, os.Args[1:], os.Environ()); err != nil {
		log.Error("demo stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, args, environ []string) error {
	if err := checkEnvironment(environ); err != nil {
		return err
	}
	o, err := parseFlags(args)
	if err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		return errors.New("demo: PostgreSQL refuses to run as root; run as an unprivileged user (the image runs as uid 1000)")
	}
	cfg, err := config.Load(os.DirFS(o.configDir))
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(o.configDir, "gated", "gazetteer.yaml"))
	if err != nil {
		return err
	}
	gaz, err := location.ParseFile(raw)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	a := newApp(ctx, o, log, cfg, gaz)
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", o.addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: a.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	log.Info("demo UI listening; starting the system", "addr", ln.Addr().String())
	a.boot()

	select {
	case <-ctx.Done():
		err = nil
	case err = <-serveErr:
	}
	log.Info("demo shutting down")
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	stop()
	a.close()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func parseFlags(args []string) (options, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var o options
	fs.StringVar(&o.pgBin, "pg-bin", "", "PostgreSQL 18 binaries directory (default: where initdb is on $PATH)")
	fs.StringVar(&o.data, "data", os.TempDir(), "writable base directory; each run's data lives in a sauti-demo-* directory under it, removed on exit")
	fs.StringVar(&o.addr, "addr", ":7860", "web UI listen address")
	fs.StringVar(&o.binDir, "bin-dir", "", "directory holding intake-gw, dispatch-worker and outbox-relay (default: this program's directory, then $PATH)")
	fs.StringVar(&o.configDir, "config-dir", "config", "configuration directory")
	fs.StringVar(&o.rulesRoot, "rules-root", ".", "directory holding triage-rules/")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.pgBin == "" {
		p, err := exec.LookPath("initdb")
		if err != nil {
			return o, errors.New("demo: --pg-bin is required (no initdb on $PATH)")
		}
		o.pgBin = filepath.Dir(p)
	}
	if o.binDir == "" {
		if exe, err := os.Executable(); err == nil {
			o.binDir = filepath.Dir(exe)
		}
	}
	for _, p := range []*string{&o.pgBin, &o.data, &o.binDir, &o.configDir, &o.rulesRoot} {
		abs, err := filepath.Abs(*p)
		if err != nil {
			return o, err
		}
		*p = abs
	}
	if err := checkPostgres(o.pgBin); err != nil {
		return o, err
	}
	for _, s := range services {
		if _, err := o.binary(s); err != nil {
			return o, err
		}
	}
	return o, nil
}

// binary finds a service command in --bin-dir, then on $PATH.
func (o options) binary(name string) (string, error) {
	p := filepath.Join(o.binDir, name)
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
		return p, nil
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("demo: %s not found in %s or on $PATH (go build -o <dir>/ ./cmd/... and pass --bin-dir <dir>)", name, o.binDir)
}

// checkPostgres requires PostgreSQL 18 in dir: the migrations use
// PostgreSQL 18-only features (ADR-0011).
func checkPostgres(dir string) error {
	for _, b := range []string{"initdb", "postgres"} {
		if _, err := exec.LookPath(filepath.Join(dir, b)); err != nil {
			return fmt.Errorf("demo: --pg-bin %s has no executable %s", dir, b)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, filepath.Join(dir, "postgres"), "--version").Output() //nolint:gosec // G204: --pg-bin is the operator's
	if err != nil {
		return fmt.Errorf("demo: postgres --version: %w", err)
	}
	if !strings.Contains(string(out), " 18.") {
		return fmt.Errorf("demo: PostgreSQL 18 is required, %s has %s", dir, strings.TrimSpace(string(out)))
	}
	return nil
}

// checkEnvironment refuses to run where the demo could reach a real system:
// SAUTI_ENV other than dev or test, or any provider credential or endpoint
// (Africa's Talking, Asterisk ARI) or store DSN already set. The demo sets
// its own settings, pointing every adapter at a local fake, and passes its
// children nothing else.
func checkEnvironment(environ []string) error {
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		if v == "" {
			continue
		}
		switch {
		case k == "SAUTI_ENV" && v != "dev" && v != "test":
			return fmt.Errorf("demo: refusing SAUTI_ENV=%q: the demo runs synthetic data in development mode only (ADR-0039)", v)
		case strings.HasPrefix(k, "SAUTI_AT_"), strings.HasPrefix(k, "SAUTI_ARI_"), k == "SAUTI_OP_DSN", k == "SAUTI_AUDIT_DSN":
			return fmt.Errorf("demo: refusing to start with %s set: the demo talks only to its own local fakes and database (ADR-0039)", k)
		}
	}
	return nil
}

// checkLoopback requires an http URL on a loopback address: where the demo
// points a provider adapter (the fake SMS endpoint).
func checkLoopback(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("demo: %q is not a URL", raw)
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "http" || ip == nil || !ip.IsLoopback() || u.User != nil {
		return fmt.Errorf("demo: refusing provider endpoint %q: the demo's adapters point only at local fakes", raw)
	}
	return nil
}

// childEnv is a child's whole environment: the demo's settings and PATH,
// never the demo's own environment.
func childEnv(vars map[string]string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "TZ=UTC"}
	for k, v := range vars {
		env = append(env, k+"="+v)
	}
	return env
}

// prefixed writes a child's output line by line under its name.
type prefixed struct {
	out    io.Writer
	prefix string
	buf    []byte
}

func (p *prefixed) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		i := bytes.IndexByte(p.buf, '\n')
		if i < 0 {
			return len(b), nil
		}
		fmt.Fprintf(p.out, "%s%s\n", p.prefix, p.buf[:i])
		p.buf = p.buf[i+1:]
	}
}
