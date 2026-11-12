// Command sautictl is the operator CLI (spec §4.2). Every subcommand uses
// the least-privileged credentials it needs and exits non-zero on failure.
//
//	sautictl migrate --store <store> --dsn <owner DSN>
//	sautictl dev-bootstrap --admin-dsn <superuser DSN> [--suffix S] [--migrate]   (dev/CI only)
//	sautictl dev-keygen --dir <dir>                                                 (dev/CI only)
//	sautictl verify-ledger --dsn <audit_reader DSN> (--devkeys-dir D | --pubkey-file F) (--witness-dir W | --no-witness)
//	sautictl verify-projections --dsn <op_readonly DSN>
//	sautictl checkpoint --dsn <audit_appender DSN> --devkeys-dir D --witness-dir W [--origin O]
//	sautictl demo m0 --admin-dsn <superuser DSN> [--config-dir config] [--keep]
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"sautihealth.com/sauti/db"
	"sautihealth.com/sauti/internal/audit"
	"sautihealth.com/sauti/internal/platform/demo"
	"sautihealth.com/sauti/internal/platform/devkeys"
	"sautihealth.com/sauti/internal/platform/env"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/auditstore"
	"sautihealth.com/sauti/internal/platform/pg/bootstrap"
	"sautihealth.com/sauti/internal/platform/pg/migrate"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
)

type command struct {
	usage string
	run   func(ctx context.Context, args []string, out io.Writer) error
}

var commands = map[string]command{
	"migrate":            {"--store <store> --dsn <owner DSN>", cmdMigrate},
	"dev-bootstrap":      {"--admin-dsn <DSN> [--suffix S] [--migrate]", cmdBootstrap},
	"dev-keygen":         {"--dir <dir>", cmdKeygen},
	"verify-ledger":      {"--dsn <audit_reader DSN> (--devkeys-dir D | --pubkey-file F) (--witness-dir W | --no-witness)", cmdVerifyLedger},
	"verify-projections": {"--dsn <op_readonly DSN>", cmdVerifyProjections},
	"checkpoint":         {"--dsn <audit_appender DSN> --devkeys-dir D --witness-dir W [--origin O]", cmdCheckpoint},
	"demo":               {"m0 --admin-dsn <DSN> [--config-dir config] [--keep]", cmdDemo},
}

func main() {
	os.Exit(Main(os.Args[1:], os.Stdout, os.Stderr))
}

// Main runs a subcommand and returns the process exit code.
func Main(args []string, out, errOut io.Writer) int {
	if len(args) == 0 || commands[args[0]].run == nil {
		fmt.Fprintln(errOut, "usage: sautictl <command> [flags]")
		for name, c := range commands {
			fmt.Fprintf(errOut, "  %-19s %s\n", name, c.usage)
		}
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := commands[args[0]].run(ctx, args[1:], out); err != nil {
		fmt.Fprintln(errOut, "sautictl "+args[0]+":", err)
		return 1
	}
	return 0
}

func flags(name string) *flag.FlagSet { return flag.NewFlagSet(name, flag.ContinueOnError) }

func cmdMigrate(ctx context.Context, args []string, out io.Writer) error {
	fs := flags("migrate")
	store := fs.String("store", "", "store name")
	dsn := fs.String("dsn", "", "owner DSN")
	if err := fs.Parse(args); err != nil {
		return err
	}
	fsys, err := db.Migrations(*store)
	if err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	applied, err := migrate.Run(ctx, conn, *store, fsys)
	fmt.Fprintf(out, "%s: applied %d migration(s) %v\n", *store, len(applied), applied)
	return err
}

func cmdBootstrap(ctx context.Context, args []string, out io.Writer) error {
	fs := flags("dev-bootstrap")
	admin := fs.String("admin-dsn", "", "superuser DSN (URL form)")
	suffix := fs.String("suffix", "", "database name suffix")
	doMigrate := fs.Bool("migrate", false, "also apply every store's migrations")
	if err := fs.Parse(args); err != nil {
		return err
	}
	password, err := env.Required("SAUTI_DEV_DB_PASSWORD")
	if err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, *admin)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	senv, err := env.Environment()
	if err != nil {
		return err
	}
	dbs, err := bootstrap.Run(ctx, conn, bootstrap.Options{Suffix: *suffix, Password: password, Env: senv})
	if err != nil {
		return err
	}
	for _, s := range pg.Stores() {
		fmt.Fprintf(out, "%-12s %s\n", s, dbs[s])
		if !*doMigrate {
			continue
		}
		cfg, err := pgx.ParseConfig(*admin)
		if err != nil {
			return err
		}
		cfg.User, cfg.Password, cfg.Database = s.Owner(), password, dbs[s]
		oc, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			return err
		}
		fsys, _ := db.Migrations(string(s))
		applied, err := migrate.Run(ctx, oc, string(s), fsys)
		_ = oc.Close(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%-12s applied %v\n", "", applied)
	}
	return nil
}

func cmdKeygen(_ context.Context, args []string, out io.Writer) error {
	fs := flags("dev-keygen")
	dir := fs.String("dir", ".devkeys", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if senv, err := env.Environment(); err != nil {
		return err
	} else if senv != "dev" && senv != "test" {
		return devkeys.ErrProductionForbidden
	}
	if err := devkeys.Generate(*dir, rand.Reader); err != nil {
		return err
	}
	fmt.Fprintf(out, "development keys written to %s/keys.json (0600) — never use outside dev/CI\n", *dir)
	return nil
}

func publicKey(devDir, file string) ([]byte, error) {
	switch {
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		return base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	case devDir != "":
		senv, err := env.Environment()
		if err != nil {
			return nil, err
		}
		st, err := devkeys.Open(devDir, senv)
		if err != nil {
			return nil, err
		}
		return st.Signer().PublicKey(), nil
	}
	return nil, errors.New("one of --devkeys-dir or --pubkey-file is required")
}

func cmdVerifyLedger(ctx context.Context, args []string, out io.Writer) error {
	fs := flags("verify-ledger")
	dsn := fs.String("dsn", "", "audit_reader DSN")
	devDir := fs.String("devkeys-dir", "", "development key directory")
	pubFile := fs.String("pubkey-file", "", "base64 ML-DSA-65 public key")
	witnessDir := fs.String("witness-dir", "", "write-once checkpoint witness directory")
	noWitness := fs.Bool("no-witness", false, "verify the chain only (a database owner's rewrite is then undetectable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pub, err := publicKey(*devDir, *pubFile)
	if err != nil {
		return err
	}
	var witnesses []audit.Checkpoint
	switch {
	case *witnessDir != "":
		notes, err := devkeys.NewFSWitness(*witnessDir).ReadAll()
		if err != nil {
			return err
		}
		for i, n := range notes {
			cp, err := audit.ParseWitnessNote(n)
			if err != nil {
				return fmt.Errorf("witness note %d: %w", i, err)
			}
			witnesses = append(witnesses, cp)
		}
	case *noWitness:
		fmt.Fprintln(out, "WARNING: --no-witness: rewritten-and-recomputed history or deleted checkpoints cannot be detected")
	default:
		return errors.New("--witness-dir is required (the write-once witness is what detects a database owner's rewrite); pass --no-witness to verify the chain only")
	}
	pool, err := pg.Connect(ctx, *dsn, "sautictl", 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	rep, err := audit.Verify(ctx, auditstore.New(pool), pub, witnesses)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "entries=%d head_seq=%d checkpoints_ok=%d checkpoints_bad=%d witnesses_ok=%d witnesses_bad=%d first_bad_seq=%d\n",
		rep.Entries, rep.HeadSeq, rep.CheckpointsOK, rep.CheckpointsBad, rep.WitnessesOK, rep.WitnessesBad, rep.FirstBadSeq)
	if !rep.OK() {
		return fmt.Errorf("LEDGER VERIFICATION FAILED: %s (runbook: docs/runbooks/audit-ledger-verification-failure.md)", rep.Problem)
	}
	fmt.Fprintln(out, "ledger OK")
	return nil
}

func cmdVerifyProjections(ctx context.Context, args []string, out io.Writer) error {
	fs := flags("verify-projections")
	dsn := fs.String("dsn", "", "op_readonly DSN")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pool, err := pg.Connect(ctx, *dsn, "sautictl", 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	checked, drift, err := opstore.VerifyProjections(ctx, opstore.New(pool))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "incidents checked=%d drift=%d\n", checked, len(drift))
	for _, d := range drift {
		fmt.Fprintln(out, "  DRIFT", d)
	}
	if len(drift) > 0 {
		return fmt.Errorf("projection drift (runbook: docs/runbooks/projection-drift.md)")
	}
	return nil
}

func cmdCheckpoint(ctx context.Context, args []string, out io.Writer) error {
	fs := flags("checkpoint")
	dsn := fs.String("dsn", "", "audit_appender DSN")
	devDir := fs.String("devkeys-dir", "", "development key directory")
	witness := fs.String("witness-dir", "", "write-once witness directory")
	origin := fs.String("origin", "sauti-dispatch", "ledger origin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	senv, err := env.Environment()
	if err != nil {
		return err
	}
	keys, err := devkeys.Open(*devDir, senv)
	if err != nil {
		return err
	}
	pool, err := pg.Connect(ctx, *dsn, "sautictl", 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	cp, err := audit.NewCheckpointer(auditstore.New(pool), keys.Signer(), devkeys.NewFSWitness(*witness), *origin, time.Now).Checkpoint(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "checkpoint seq=%d head=%s key=%s\n", cp.Seq, base64.StdEncoding.EncodeToString(cp.Head[:]), cp.KeyID)
	return nil
}

func cmdDemo(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "m0" {
		return errors.New("usage: sautictl demo m0 --admin-dsn <DSN>")
	}
	fs := flags("demo")
	admin := fs.String("admin-dsn", env.Default("SAUTI_TEST_PG_DSN", ""), "superuser DSN (URL form)")
	cfgDir := fs.String("config-dir", "config", "configuration directory")
	keep := fs.Bool("keep", false, "keep the demo databases")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *admin == "" {
		return errors.New("--admin-dsn (or SAUTI_TEST_PG_DSN) is required; without a server run: go test -tags integration -v ./tests/e2e/")
	}
	fmt.Fprintln(out, "Sauti Dispatch — M0 demo (spec §12.2)")
	_, err := demo.RunM0(ctx, demo.Options{AdminDSN: *admin, ConfigDir: *cfgDir, Out: out, Keep: *keep})
	if err == nil {
		fmt.Fprintln(out, "M0 demo PASSED")
	}
	return err
}
