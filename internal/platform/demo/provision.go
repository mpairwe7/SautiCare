package demo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"sautihealth.com/sauti/db"
	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/devkeys"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/bootstrap"
	"sautihealth.com/sauti/internal/platform/pg/migrate"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/responder"
)

// Stores is a freshly bootstrapped and migrated set of the five stores on one
// development server, with the configuration's gazetteer loaded. Shared by
// the M0 demo and cmd/demo (the Hugging Face Space demo, ADR-0039).
type Stores struct {
	admin    url.URL
	password string
	DBs      map[pg.Store]string // store → database name
}

// DSN returns a URL DSN for role on store's database.
func (s Stores) DSN(store pg.Store, role string) string {
	u := s.admin
	u.User = url.UserPassword(role, s.password)
	u.Path = "/" + s.DBs[store]
	return u.String()
}

// SuperDSN returns the superuser DSN of store's database.
func (s Stores) SuperDSN(store pg.Store) string {
	u := s.admin
	u.Path = "/" + s.DBs[store]
	return u.String()
}

// provisionTimeout bounds a whole Provision call (a variable so tests can
// shorten it).
var provisionTimeout = 2 * time.Minute

// Provision creates the five stores under a random suffix on the server that
// adminConn (a superuser connection to adminDSN) is connected to, migrates
// each as its owner and loads the gazetteer of configDir. Every role gets
// password: on a shared server it must be the server's established one, since
// roles are server-global. DBs is set as soon as the databases exist, even
// when a later step fails, so the caller can drop them. The call is bounded
// by provisionTimeout, or the caller's earlier deadline. Development and test
// only: bootstrap refuses any other senv.
func Provision(ctx context.Context, adminConn *pgx.Conn, adminDSN, senv, password, configDir string) (Stores, error) {
	ctx, cancel := context.WithTimeout(ctx, provisionTimeout)
	defer cancel()
	admin, err := url.Parse(adminDSN)
	if err != nil || admin.Scheme == "" {
		return Stores{}, errors.New("demo: admin DSN must be a postgres:// URL")
	}
	st := Stores{admin: *admin, password: password}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	if st.DBs, err = bootstrap.Run(ctx, adminConn, bootstrap.Options{Suffix: "_demo" + hex.EncodeToString(suffix), Password: password, Env: senv}); err != nil {
		return st, err
	}
	for _, s := range pg.Stores() {
		fsys, err := db.Migrations(string(s))
		if err != nil {
			return st, err
		}
		c, err := pgx.Connect(ctx, st.DSN(s, s.Owner()))
		if err != nil {
			return st, err
		}
		_, err = migrate.Run(ctx, c, string(s), fsys)
		_ = c.Close(ctx)
		if err != nil {
			return st, err
		}
	}
	raw, err := os.ReadFile(filepath.Join(configDir, "gated", "gazetteer.yaml"))
	if err != nil {
		return st, err
	}
	gaz, err := location.ParseFile(raw)
	if err != nil {
		return st, err
	}
	owner, err := pgx.Connect(ctx, st.DSN(pg.StoreOperational, pg.StoreOperational.Owner()))
	if err != nil {
		return st, err
	}
	_, err = opstore.LoadGazetteer(ctx, owner, gaz, raw)
	_ = owner.Close(ctx)
	return st, err
}

// Responder is a synthetic responder (development only). Label is a role
// label such as "VHT 1", stored as the name: never a real-looking name.
type Responder struct {
	Role         responder.Role
	Label, Phone string
	Ward, Stage  string
}

// RegisterResponders registers rs through the registration service (console,
// as op_console) — certified now, insured for 30 days, kit OK — and switches
// each on for 8 hours through intake (as op_intake, the responder menu's
// path). It returns their IDs in the order of rs.
func RegisterResponders(ctx context.Context, console, intake *opstore.Store, keys *devkeys.Store, rs []Responder) ([]uuid.UUID, error) {
	registry := responder.NewService(console, crypto.Identifiers{Env: crypto.NewEnvelope(keys, rand.Reader)})
	now := time.Now()
	insured := now.AddDate(0, 0, 30)
	ids := make([]uuid.UUID, 0, len(rs))
	for _, d := range rs {
		phone, err := pii.ParsePhone(d.Phone)
		if err != nil {
			return nil, err
		}
		reg, err := registry.Register(ctx, responder.RegisterCmd{Role: d.Role, Name: pii.NewPersonName(d.Label),
			Phone: phone, Ward: d.Ward, Stage: d.Stage, CertifiedAt: &now, InsuredUntil: &insured, KitOK: true})
		if err != nil {
			return nil, err
		}
		if _, err := intake.SetAvailability(ctx, reg.ID, true, "ussd"); err != nil {
			return nil, err
		}
		ids = append(ids, reg.ID)
	}
	return ids, nil
}
