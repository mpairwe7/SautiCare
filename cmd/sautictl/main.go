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
//	sautictl gazetteer load --file config/gated/gazetteer.yaml --dsn <operational owner DSN>
//	sautictl triage keygen --out <seed file> --key-id dev-<name>                  (dev/CI only)
//	sautictl triage sign --key <seed file> --key-id <id> --rules triage-rules/tool1.yaml
//	sautictl incident split --dsn <op_console DSN> --survivor <id> --member <id> --staff <staff subject UUID>
//	sautictl responder add --dsn <op_console DSN> --devkeys-dir D --role vht|transporter --name N --phone P --ward W --stage S   (dev/CI only)
//	sautictl responder available --dsn <op_console DSN> --id <responder id> [--off]                                             (dev/CI only)
//	sautictl dispatch status --dsn <op_readonly or op_console DSN>
//	sautictl dispatch suspend|resume --dsn <op_console DSN> --reason <code> --staff <staff subject UUID> [--config config]
package main

import (
	"context"
	"crypto/mldsa"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"sautihealth.com/sauti/db"
	"sautihealth.com/sauti/internal/audit"
	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/demo"
	"sautihealth.com/sauti/internal/platform/devkeys"
	"sautihealth.com/sauti/internal/platform/env"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/auditstore"
	"sautihealth.com/sauti/internal/platform/pg/bootstrap"
	"sautihealth.com/sauti/internal/platform/pg/migrate"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/responder"
	"sautihealth.com/sauti/internal/triage"
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
	"gazetteer":          {"load --file <gazetteer.yaml> --dsn <operational owner DSN>", cmdGazetteer},
	"dispatch":           {"status --dsn <DSN> | suspend|resume --dsn <op_console DSN> --reason <code> --staff <UUID> [--config D]", cmdDispatch},
	"responder":          {"add --dsn <op_console DSN> --devkeys-dir D --role R --name N --phone P --ward W --stage S | available --dsn <DSN> --id <id> [--off]", cmdResponder},
	"incident":           {"split --dsn <op_console DSN> --survivor <id> --member <id> --staff <staff UUID>", cmdIncident},
	"triage":             {"keygen --out <file> --key-id dev-<name> | sign --key <file> --key-id <id> --rules <bundle>", cmdTriage},
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

// blindWarning ends every warning that verification runs without witnesses.
const blindWarning = "rewritten-and-recomputed history or deleted checkpoints cannot be detected"

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
	case *witnessDir != "" && *noWitness:
		return errors.New("--witness-dir and --no-witness are mutually exclusive")
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
		if len(witnesses) == 0 {
			// Empty, unmounted or mistyped: as blind as --no-witness, so it
			// must not end in "ledger OK" (CodeRabbit PR #4 and PR #5). Before
			// the first checkpoint exists, pass --no-witness instead.
			fmt.Fprintln(out, "WARNING: --witness-dir holds no checkpoints: "+blindWarning)
			return errors.New("--witness-dir holds no checkpoints (an unmounted volume or a wrong path), so verification is refused; before the first checkpoint exists, pass --no-witness instead")
		}
	case *noWitness:
		fmt.Fprintln(out, "WARNING: --no-witness: "+blindWarning)
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
	if *witness == "" {
		return errors.New("--witness-dir is required (the checkpoint is anchored on it and written to it)")
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
	// Anchor on the latest witnessed checkpoint before signing, so a
	// rewritten or truncated history is refused, never signed and witnessed
	// (CodeRabbit PR #4). The witness is read before any query.
	cper, _, err := audit.NewAnchoredCheckpointer(auditstore.New(pool), keys.Signer(), devkeys.NewFSWitness(*witness), *origin, time.Now)
	if err != nil {
		return fmt.Errorf("witness store failed verification; refusing to sign (runbook: docs/runbooks/audit-ledger-verification-failure.md): %w", err)
	}
	cp, err := cper.Checkpoint(ctx)
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
	// Refused before any connection: the demo provisions server-global
	// roles and databases (CodeRabbit PR #4).
	senv, err := env.Environment()
	if err != nil {
		return err
	}
	if senv != "dev" && senv != "test" {
		return fmt.Errorf("refusing SAUTI_ENV=%s: the demo provisions server-global roles and databases (dev and test only)", senv)
	}
	password, err := env.Required("SAUTI_DEV_DB_PASSWORD")
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Sauti Dispatch — M0 demo (spec §12.2)")
	_, err = demo.RunM0(ctx, demo.Options{AdminDSN: *admin, Env: senv, RolePassword: password, ConfigDir: *cfgDir, Out: out, Keep: *keep})
	if err == nil {
		fmt.Fprintln(out, "M0 demo PASSED")
	}
	return err
}

// cmdGazetteer loads a gazetteer version into the operational store. The file
// is validated before any connection is made; loading the same version twice
// is a no-op and different content under a loaded version is refused.
func cmdGazetteer(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "load" {
		return errors.New("usage: sautictl gazetteer load --file <gazetteer.yaml> --dsn <owner DSN>")
	}
	fs := flags("gazetteer load")
	file := fs.String("file", "config/gated/gazetteer.yaml", "gazetteer file")
	dsn := fs.String("dsn", "", "operational store owner DSN")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	f, err := location.ParseFile(raw)
	if err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("--dsn (the operational store owner) is required")
	}
	conn, err := pgx.Connect(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	loaded, err := opstore.LoadGazetteer(ctx, conn, f, raw)
	if err != nil {
		return err
	}
	if loaded {
		fmt.Fprintf(out, "gazetteer %s (%s): loaded %d wards, %d landmarks\n", f.Version, f.Status, len(f.Wards), len(f.Landmarks))
	} else {
		fmt.Fprintf(out, "gazetteer %s: already loaded, unchanged\n", f.Version)
	}
	return nil
}

// cmdTriage generates development rule-signing keys and signs rule bundles.
// Production bundles are signed with the clinical sign-off key held in
// OpenBao (P0); keygen only ever makes "dev-" keys, which a production
// intake-gw refuses.
func cmdTriage(_ context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: sautictl triage keygen|sign [flags]")
	}
	switch args[0] {
	case "keygen":
		fs := flags("triage keygen")
		file := fs.String("out", "", "seed file to create (0600, never overwritten)")
		keyID := fs.String("key-id", "", `key ID, must start with "dev-"`)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if senv, err := env.Environment(); err != nil {
			return err
		} else if senv != "dev" && senv != "test" {
			return devkeys.ErrProductionForbidden
		}
		if *file == "" || !strings.HasPrefix(*keyID, "dev-") {
			return errors.New(`--out and a --key-id starting with "dev-" are required`)
		}
		sk, err := mldsa.GenerateKey(mldsa.MLDSA65())
		if err != nil {
			return err
		}
		f, err := os.OpenFile(*file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("%w (refusing to overwrite a signing key)", err)
		}
		if _, err := f.Write(sk.Bytes()); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: %s\n", *keyID, base64.StdEncoding.EncodeToString(sk.PublicKey().Bytes()))
		return nil
	case "sign":
		fs := flags("triage sign")
		file := fs.String("key", "", "seed file")
		keyID := fs.String("key-id", "", "key ID recorded in the signature")
		rulesPath := fs.String("rules", "triage-rules/tool1.yaml", "rule bundle")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		seed, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		sk, err := mldsa.NewPrivateKey(mldsa.MLDSA65(), seed)
		if err != nil {
			return err
		}
		doc, err := os.ReadFile(*rulesPath)
		if err != nil {
			return err
		}
		sig, err := triage.Sign(sk, *keyID, doc)
		if err != nil {
			return err
		}
		// Never write a signature for a bundle that would not load.
		if _, err := triage.Load(doc, sig, map[string][]byte{*keyID: sk.PublicKey().Bytes()}); err != nil {
			return err
		}
		// The signature is public and committed next to the bundle.
		if err := os.WriteFile(*rulesPath+".sig", sig, 0o644); err != nil { //nolint:gosec // G306: a public, committed file
			return err
		}
		fmt.Fprintf(out, "signed %s with %s\n", *rulesPath, *keyID)
		return nil
	}
	return fmt.Errorf("unknown triage subcommand %q", args[0])
}

// cmdIncident holds coordinator actions until the console exists (M5).
// "split" separates a wrongly merged call from its survivor (Build Prompt
// §10.3), logged with the coordinator's identity-provider subject.
func cmdIncident(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "split" {
		return errors.New("usage: sautictl incident split --dsn <op_console DSN> --survivor <id> --member <id> --staff <UUID>")
	}
	fs := flags("incident split")
	dsn := fs.String("dsn", "", "operational store DSN as op_console")
	survivorFlag := fs.String("survivor", "", "the incident the call was merged into")
	memberFlag := fs.String("member", "", "the wrongly merged incident")
	staffFlag := fs.String("staff", "", "the coordinator's staff identity (identity-provider subject UUID)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	ids := make([]uuid.UUID, 3)
	for i, v := range []struct{ name, val string }{{"--survivor", *survivorFlag}, {"--member", *memberFlag}, {"--staff", *staffFlag}} {
		id, err := uuid.Parse(v.val)
		if err != nil {
			return fmt.Errorf("%s must be a UUID", v.name)
		}
		ids[i] = id
	}
	if ids[0] == ids[1] {
		return errors.New("--survivor and --member must differ")
	}
	if *dsn == "" {
		return errors.New("--dsn (op_console) is required")
	}
	pool, err := pg.Connect(ctx, *dsn, "sautictl", 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := opstore.New(pool).SplitIncident(ctx, ids[0], ids[1], ids[2]); err != nil {
		return err
	}
	fmt.Fprintf(out, "split %s from %s (logged with staff %s)\n", ids[1], ids[0], ids[2])
	return nil
}

// cmdResponder registers synthetic responders and switches availability for
// development and tests. Real registration is the console's (M5), with the
// production key provider.
func cmdResponder(ctx context.Context, args []string, out io.Writer) error {
	if senv, err := env.Environment(); err != nil {
		return err
	} else if senv != "dev" && senv != "test" {
		return devkeys.ErrProductionForbidden
	}
	if len(args) == 0 || (args[0] != "add" && args[0] != "available") {
		return errors.New("usage: sautictl responder add|available [flags]")
	}
	fs := flags("responder " + args[0])
	dsn := fs.String("dsn", "", "operational store DSN as op_console")
	switch args[0] {
	case "add":
		keysDir := fs.String("devkeys-dir", ".devkeys", "development key directory")
		roleFlag := fs.String("role", "", "vht or transporter")
		name := fs.String("name", "", "display name (synthetic in development)")
		phoneFlag := fs.String("phone", "", "phone number")
		ward := fs.String("ward", "", "gazetteer ward ID")
		stage := fs.String("stage", "", "gazetteer stage ID")
		insuredDays := fs.Int("insured-days", 365, "insurance cover from today, in days")
		kit := fs.Bool("kit", true, "kit OK")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		role, err := responder.ParseRole(*roleFlag)
		if err != nil {
			return err
		}
		phone, err := pii.ParsePhone(*phoneFlag)
		if err != nil {
			return fmt.Errorf("--phone: %w", err)
		}
		if *dsn == "" || *name == "" || *ward == "" || *stage == "" {
			return errors.New("--dsn, --name, --ward and --stage are required")
		}
		keys, err := devkeys.Open(*keysDir, "dev")
		if err != nil {
			return err
		}
		pool, err := pg.Connect(ctx, *dsn, "sautictl", 2)
		if err != nil {
			return err
		}
		defer pool.Close()
		now := time.Now()
		insured := now.AddDate(0, 0, *insuredDays)
		reg, err := responder.NewService(opstore.New(pool), crypto.Identifiers{Env: crypto.NewEnvelope(keys, rand.Reader)}).Register(ctx,
			responder.RegisterCmd{Role: role, Name: pii.NewPersonName(*name), Phone: phone, Ward: *ward, Stage: *stage,
				CertifiedAt: &now, InsuredUntil: &insured, KitOK: *kit})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "registered %s responder %s\n", role, reg.ID)
		return nil
	default:
		idFlag := fs.String("id", "", "responder ID")
		off := fs.Bool("off", false, "switch off instead of on")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		id, err := uuid.Parse(*idFlag)
		if err != nil {
			return errors.New("--id must be a UUID")
		}
		if *dsn == "" {
			return errors.New("--dsn is required")
		}
		pool, err := pg.Connect(ctx, *dsn, "sautictl", 2)
		if err != nil {
			return err
		}
		defer pool.Close()
		p, err := opstore.New(pool).SetAvailability(ctx, id, !*off, "console")
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "responder %s available until %s\n", id, p.Until.Format(time.RFC3339))
		return nil
	}
}

var reasonCode = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

// cmdDispatch shows, suspends or resumes dispatch (O6; ADR-0026). Suspending
// and resuming are a coordinator's decisions, recorded with their staff
// identity in the dated register and the audit ledger. In production,
// suspending is refused until the message callers then get is approved.
func cmdDispatch(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || (args[0] != "status" && args[0] != "suspend" && args[0] != "resume") {
		return errors.New("usage: sautictl dispatch status|suspend|resume [flags]")
	}
	fs := flags("dispatch " + args[0])
	dsn := fs.String("dsn", "", "operational store DSN (op_console; op_readonly for status)")
	reason := fs.String("reason", "", "reason code, e.g. coordinator_decision")
	staffFlag := fs.String("staff", "", "the coordinator's staff subject UUID")
	cfgDir := fs.String("config", "config", "configuration directory")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("--dsn is required")
	}
	var staff uuid.UUID
	if args[0] != "status" {
		if !reasonCode.MatchString(*reason) {
			return errors.New("--reason must be a code (lower case, digits, _ . -)")
		}
		var err error
		if staff, err = uuid.Parse(*staffFlag); err != nil {
			return errors.New("--staff must be the coordinator's staff subject UUID")
		}
	}
	if args[0] == "suspend" {
		senv, err := env.Environment()
		if err != nil {
			return err
		}
		if senv == "production" {
			cfg, err := config.Load(os.DirFS(*cfgDir))
			if err != nil {
				return err
			}
			if err := cfg.SuspensionReady(); err != nil {
				return fmt.Errorf("dispatch: suspension is not enabled in production until the Medical Director and PI approve its message: %w", err)
			}
		}
	}
	pool, err := pg.Connect(ctx, *dsn, "sautictl", 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	store := opstore.New(pool)
	switch args[0] {
	case "suspend":
		changed, err := store.SuspendDispatch(ctx, *reason, staff)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "dispatch suspended (changed=%v)\n", changed)
	case "resume":
		changed, err := store.ResumeDispatch(ctx, *reason, staff)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "dispatch resumed (changed=%v)\n", changed)
	default:
		on, err := store.DispatchSuspended(ctx)
		if err != nil {
			return err
		}
		state := "active"
		if on {
			state = "SUSPENDED"
		}
		fmt.Fprintf(out, "dispatch: %s\n", state)
		log, err := store.ControlLog(ctx, 10)
		if err != nil {
			return err
		}
		for _, e := range log {
			who := "automatic"
			if e.StaffID != nil {
				who = e.StaffID.String()
			}
			fmt.Fprintf(out, "%s  %s %s  by %s", e.At.Format(time.RFC3339), e.Action, e.Reason, who)
			if e.Action == "stopping_rule_breached" {
				fmt.Fprintf(out, "  (%d of %d red incidents failed)", e.Failures, e.Window)
			}
			fmt.Fprintln(out)
		}
	}
	return nil
}
