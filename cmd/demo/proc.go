package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

// proc is a child process of the demo.
type proc struct {
	name string
	cmd  *exec.Cmd
	done chan struct{}
	err  error // the exit status, set before done closes
}

func startProc(name, path string, args, env []string, log io.Writer) (*proc, error) {
	cmd := exec.Command(path, args...) //nolint:gosec,noctx // G204: our own binaries; the demo stops them itself
	cmd.Env = env
	w := &prefixed{out: log, prefix: "[" + name + "] "}
	cmd.Stdout, cmd.Stderr = w, w
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("demo: start %s: %w", name, err)
	}
	p := &proc{name: name, cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

func (p *proc) running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// stop sends sig and waits up to grace before killing the process.
func (p *proc) stop(sig os.Signal, grace time.Duration) {
	if p == nil {
		return
	}
	_ = p.cmd.Process.Signal(sig)
	select {
	case <-p.done:
	case <-time.After(grace):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

// waitReady polls a health URL until it answers 200, the process exits or
// ctx ends.
func waitReady(ctx context.Context, p *proc, healthURL string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		if !p.running() {
			return fmt.Errorf("demo: %s exited during start-up: %w", p.name, p.err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err != nil {
			return err
		}
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("demo: %s not ready: %w", p.name, ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func freePort() (string, error) {
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = l.Close() }()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port), nil
}

// secret is a fresh random value: every password, token and key of a run.
func secret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// startPostgres initialises a cluster in dir/pgdata with a random superuser
// password and starts it on a free loopback port with no Unix socket. The
// data is throwaway, so durability is switched off for speed.
func startPostgres(ctx context.Context, bin, dir string, log io.Writer) (*proc, string, error) {
	password := secret()
	pwfile := filepath.Join(dir, "pwfile")
	if err := os.WriteFile(pwfile, []byte(password+"\n"), 0o600); err != nil {
		return nil, "", err
	}
	data := filepath.Join(dir, "pgdata")
	initdb := exec.CommandContext(ctx, filepath.Join(bin, "initdb"), "-D", data, "-U", "postgres", "--pwfile", pwfile, //nolint:gosec // G204: --pg-bin is the operator's
		"--auth", "scram-sha-256", "-E", "UTF8", "--no-locale", "--no-instructions")
	initdb.Env = childEnv(nil)
	w := &prefixed{out: log, prefix: "[initdb] "}
	initdb.Stdout, initdb.Stderr = w, w
	err := initdb.Run()
	_ = os.Remove(pwfile)
	if err != nil {
		return nil, "", fmt.Errorf("demo: initdb: %w", err)
	}
	port, err := freePort()
	if err != nil {
		return nil, "", err
	}
	p, err := startProc("postgres", filepath.Join(bin, "postgres"), []string{"-D", data, "-p", port,
		"-c", "listen_addresses=127.0.0.1", "-c", "unix_socket_directories=",
		"-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off"}, childEnv(nil), log)
	if err != nil {
		return nil, "", err
	}
	admin := (&url.URL{Scheme: "postgres", User: url.UserPassword("postgres", password), Host: "127.0.0.1:" + port,
		Path: "/postgres", RawQuery: "sslmode=disable"}).String()
	deadline := time.Now().Add(60 * time.Second)
	for {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		c, err := pgx.Connect(cctx, admin)
		cancel()
		if err == nil {
			_ = c.Close(ctx)
			return p, admin, nil
		}
		if !p.running() || ctx.Err() != nil || time.Now().After(deadline) {
			p.stop(syscall.SIGINT, 15*time.Second)
			return nil, "", fmt.Errorf("demo: PostgreSQL did not start: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
