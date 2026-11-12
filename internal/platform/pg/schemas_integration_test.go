//go:build integration

package pg_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

func as(t *testing.T, c *pgtest.TestCluster, s pg.Store, role string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), c.DSN(s, role))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func code(err error) string {
	var e *pgconn.PgError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestAllStoresMigrateFromZero(t *testing.T) {
	c := pgtest.Cluster(t)
	want := map[pg.Store][]string{
		pg.StoreOperational: {"0000_schema", "0001_core", "0002_write_path", "0003_grants"},
		pg.StoreAudit:       {"0000_schema", "0001_ledger"},
		pg.StoreAnalytic:    {"0000_schema", "0001_meds"},
		pg.StoreLinkage:     {"0000_schema", "0001_vault"},
		pg.StoreMedia:       {"0000_schema", "0001_media"},
	}
	for s, versions := range want {
		rows, err := c.Owner(t, s).Query(context.Background(), "SELECT version FROM public.schema_migrations ORDER BY version")
		if err != nil {
			t.Fatal(err)
		}
		got, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, versions) {
			t.Errorf("%s applied %v, want %v", s, got, versions)
		}
	}
}

const insertMEDS = `INSERT INTO analytic.meds_records
	(study_id, parish, landmark_code, time_of_call, category, severity, responder_types,
	 logic_version, zone_set_version, consent_status)
	VALUES ($1, 'Bwaise I', 'LM-001', now(), 'trauma', 'red', ARRAY['vht','transporter'],
	        'tr-test', 'zs-1', $2)`

const insertWaiver = `INSERT INTO analytic.meds_records
	(study_id, parish, time_of_call, logic_version, zone_set_version, consent_status, waiver_reason)
	VALUES ($1, 'Bwaise II', now(), 'tr-test', 'zs-1', 'waiver', $2)`

// Only records with consent, guardian permission/assent or a waiver may
// enter the analytic store (Build Prompt rule 3.10; Guide §14).
func TestAnalyticRejectsUnconsentedRecord(t *testing.T) {
	c := pgtest.Cluster(t)
	w := as(t, c, pg.StoreAnalytic, "an_writer")
	ctx := context.Background()
	for _, status := range []string{"pending", "declined", "withdrawn", "late_contact"} {
		if _, err := w.Exec(ctx, insertMEDS, uuid.NewV4(), status); code(err) != "23514" {
			t.Errorf("consent_status %q: err = %v, want check violation 23514", status, err)
		}
	}
	for _, status := range []string{"consented", "guardian_permission", "assent"} {
		if _, err := w.Exec(ctx, insertMEDS, uuid.NewV4(), status); err != nil {
			t.Errorf("consent_status %q rejected: %v", status, err)
		}
	}
	// A waiver must carry one of the reasons the ethics committee allows.
	if _, err := w.Exec(ctx, insertMEDS, uuid.NewV4(), "waiver"); code(err) != "23514" {
		t.Errorf("waiver without reason: err = %v, want 23514", err)
	}
	if _, err := w.Exec(ctx, insertWaiver, uuid.NewV4(), "deceased"); err != nil {
		t.Errorf("waiver with reason rejected: %v", err)
	}
	if _, err := w.Exec(ctx, insertWaiver, uuid.NewV4(), "convenient"); code(err) != "23514" {
		t.Errorf("waiver with unknown reason: err = %v, want 23514", err)
	}
}

// The linkage vault opens only for one of four stated reasons, and every
// attempt — granted or not — is logged (ISP §5.5, Build Prompt §13).
func TestLinkageResolveRequiresAllowedReason(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := context.Background()
	study, inc := uuid.NewV4(), uuid.NewV7()
	if _, err := as(t, c, pg.StoreLinkage, "lk_writer").Exec(ctx,
		"INSERT INTO linkage.pairs (study_id, incident_id) VALUES ($1, $2)", study, inc); err != nil {
		t.Fatal(err)
	}
	r := as(t, c, pg.StoreLinkage, "lk_reader")
	var got *uuid.UUID
	if err := r.QueryRow(ctx, "SELECT linkage.resolve_incident($1, 'curiosity', 'staff-pi')", study).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("disallowed reason resolved to %v", got)
	}
	if err := r.QueryRow(ctx, "SELECT linkage.resolve_incident($1, 'withdrawal', 'staff-pi')", study).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != inc {
		t.Fatalf("allowed reason resolved to %v, want %v", got, inc)
	}
	var total, granted int
	if err := c.Owner(t, pg.StoreLinkage).QueryRow(ctx,
		"SELECT count(*), count(*) FILTER (WHERE granted) FROM linkage.access_log WHERE study_id = $1", study).
		Scan(&total, &granted); err != nil {
		t.Fatal(err)
	}
	if total != 2 || granted != 1 {
		t.Fatalf("access_log total=%d granted=%d, want 2 and 1", total, granted)
	}
}

func TestLinkageRolesAreNarrow(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := context.Background()
	if _, err := as(t, c, pg.StoreLinkage, "lk_reader").Exec(ctx, "SELECT * FROM linkage.pairs"); code(err) != "42501" {
		t.Errorf("lk_reader direct SELECT: %v, want 42501", err)
	}
	if _, err := as(t, c, pg.StoreLinkage, "lk_writer").Exec(ctx, "SELECT * FROM linkage.pairs"); code(err) != "42501" {
		t.Errorf("lk_writer SELECT: %v, want 42501", err)
	}
	if _, err := as(t, c, pg.StoreLinkage, "lk_writer").Exec(ctx, "DELETE FROM linkage.access_log"); code(err) != "42501" {
		t.Errorf("lk_writer DELETE access_log: %v, want 42501", err)
	}
}

// A withdrawal's study ID stays suppressed forever: the ledger is replayed
// after every analytic restore (Build Prompt §13).
func TestSuppressionLedgerAppendOnly(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := context.Background()
	w := as(t, c, pg.StoreAnalytic, "an_writer")
	id := uuid.NewV4()
	if _, err := w.Exec(ctx, "INSERT INTO analytic.suppression_ledger (study_id, reason) VALUES ($1, 'withdrawal')", id); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(ctx, "DELETE FROM analytic.suppression_ledger"); code(err) != "42501" {
		t.Errorf("an_writer DELETE: %v, want 42501", err)
	}
	if _, err := c.Owner(t, pg.StoreAnalytic).Exec(ctx, "DELETE FROM analytic.suppression_ledger"); code(err) != "SA403" {
		t.Errorf("owner DELETE: %v, want SA403", err)
	}
}

func TestAnalyticReaderIsReadOnly(t *testing.T) {
	c := pgtest.Cluster(t)
	if _, err := as(t, c, pg.StoreAnalytic, "an_reader").Exec(context.Background(), insertMEDS, uuid.NewV4(), "consented"); code(err) != "42501" {
		t.Fatalf("an_reader insert: %v, want 42501", err)
	}
}

// Every recording carries its destruction deadline (RMRP §3: interview audio
// at most 12 months) and its own key ID.
func TestMediaRecordingsRequireDestroyByAndKey(t *testing.T) {
	c := pgtest.Cluster(t)
	w := as(t, c, pg.StoreMedia, "md_writer")
	ctx := context.Background()
	if _, err := w.Exec(ctx, "INSERT INTO media.recordings (key_id, consent_ref) VALUES ('k1', 'C-1')"); code(err) != "23502" {
		t.Errorf("recording without destroy_by: %v, want 23502", err)
	}
	if _, err := w.Exec(ctx, "INSERT INTO media.recordings (key_id, consent_ref, destroy_by) VALUES ('k1', 'C-1', current_date + 365)"); err != nil {
		t.Errorf("valid recording rejected: %v", err)
	}
	if _, err := w.Exec(ctx, "INSERT INTO media.recordings (key_id, consent_ref, destroy_by) VALUES ('k1', 'C-1', current_date + 400)"); code(err) != "23514" {
		t.Errorf("destroy_by beyond 12 months: %v, want 23514", err)
	}
}
