//go:build integration

package demo

import (
	"context"
	"errors"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

// CodeRabbit PR #15: Provision bounds itself, so a stalled database (here
// bootstrap's role lock, held by another session) fails it promptly even
// when the caller's context carries no deadline, as cmd/demo's does not.
func TestProvisionHasItsOwnDeadline(t *testing.T) {
	admin, holder := pgtest.AdminConn(t), pgtest.AdminConn(t)
	if _, err := holder.Exec(pgtest.Context(t), "SELECT pg_advisory_lock(7347000)"); err != nil { // bootstrap's roleLock
		t.Fatal(err)
	}
	old := provisionTimeout
	provisionTimeout = 500 * time.Millisecond
	t.Cleanup(func() { provisionTimeout = old })
	start := time.Now()
	_, err := Provision(context.Background(), admin, pgtest.AdminURL(t), "test", pgtest.RolePassword(t), "../../../config")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 10*time.Second {
		t.Fatalf("err = %v after %v; want the provisioning deadline", err, time.Since(start))
	}
}
