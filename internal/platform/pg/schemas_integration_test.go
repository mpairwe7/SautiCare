//go:build integration

package pg_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

func as(t *testing.T, c *pgtest.TestCluster, s pg.Store, role string) *pgx.Conn {
	t.Helper()
	return pgtest.Connect(t, c.DSN(s, role))
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
		pg.StoreOperational: {"0000_schema", "0001_core", "0002_write_path", "0003_grants", "0004_default_privileges", "0005_salted_payload_hash",
			"0027_review_pr3_coderabbit"},
		pg.StoreAudit:    {"0000_schema", "0001_ledger", "0002_default_privileges", "0003_hash_at"},
		pg.StoreAnalytic: {"0000_schema", "0001_meds", "0002_default_privileges"},
		pg.StoreLinkage:  {"0000_schema", "0001_vault", "0002_default_privileges"},
		pg.StoreMedia:    {"0000_schema", "0001_media", "0002_default_privileges"},
	}
	ctx := pgtest.Context(t)
	for s, versions := range want {
		rows, err := c.Owner(t, s).Query(ctx, "SELECT version FROM public.schema_migrations ORDER BY version")
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
	        'tr-test+sha256:0123456789ab', 'zs-1', $2)`

const insertWaiver = `INSERT INTO analytic.meds_records
	(study_id, parish, time_of_call, logic_version, zone_set_version, consent_status, waiver_reason)
	VALUES ($1, 'Bwaise II', now(), 'tr-test+sha256:0123456789ab', 'zs-1', 'waiver', $2)`

// Only records with consent, guardian permission/assent or a waiver may
// enter the analytic store (Build Prompt rule 3.10; Guide §14).
func TestAnalyticRejectsUnconsentedRecord(t *testing.T) {
	c := pgtest.Cluster(t)
	w := as(t, c, pg.StoreAnalytic, "an_writer")
	ctx := pgtest.Context(t)
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
	ctx := pgtest.Context(t)
	study, inc := uuid.NewV4(), uuid.NewV7()
	if _, err := as(t, c, pg.StoreLinkage, "lk_writer").Exec(ctx,
		"INSERT INTO linkage.pairs (study_id, incident_id) VALUES ($1, $2)", study, inc); err != nil {
		t.Fatal(err)
	}
	r := as(t, c, pg.StoreLinkage, "lk_reader")
	staff := uuid.NewV4().String()
	var got *uuid.UUID
	if err := r.QueryRow(ctx, "SELECT linkage.resolve_incident($1, 'curiosity', $2)", study, staff).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("disallowed reason resolved to %v", got)
	}
	if err := r.QueryRow(ctx, "SELECT linkage.resolve_incident($1, 'withdrawal', $2)", study, staff).Scan(&got); err != nil {
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
	ctx := pgtest.Context(t)
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
	ctx := pgtest.Context(t)
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
	if _, err := as(t, c, pg.StoreAnalytic, "an_reader").Exec(pgtest.Context(t), insertMEDS, uuid.NewV4(), "consented"); code(err) != "42501" {
		t.Fatalf("an_reader insert: %v, want 42501", err)
	}
}

// Every recording carries its destruction deadline (RMRP §3: interview audio
// at most 12 months) and its own key ID.
func TestMediaRecordingsRequireDestroyByAndKey(t *testing.T) {
	c := pgtest.Cluster(t)
	w := as(t, c, pg.StoreMedia, "md_writer")
	ctx := pgtest.Context(t)
	if _, err := w.Exec(ctx, "INSERT INTO media.recordings (key_id, consent_ref) VALUES ('k1', 'C-1')"); code(err) != "23502" {
		t.Errorf("recording without destroy_by: %v, want 23502", err)
	}
	if _, err := w.Exec(ctx, "INSERT INTO media.recordings (key_id, consent_ref, destroy_by) VALUES ('k1', 'C-1', (now() AT TIME ZONE 'UTC')::date + 365)"); err != nil {
		t.Errorf("valid recording rejected: %v", err)
	}
	if _, err := w.Exec(ctx, "INSERT INTO media.recordings (key_id, consent_ref, destroy_by) VALUES ('k1', 'C-1', (now() AT TIME ZONE 'UTC')::date + 400)"); code(err) != "23514" {
		t.Errorf("destroy_by beyond 12 months: %v, want 23514", err)
	}
}

// CodeRabbit PR #3: the retention limit is twelve calendar months from the
// UTC creation date, not a fixed 366 days.
func TestMediaRetentionIsTwelveCalendarMonths(t *testing.T) {
	c := pgtest.Cluster(t)
	owner := c.Owner(t, pg.StoreMedia) // only the owner may set created_at
	ctx := pgtest.Context(t)
	const q = "INSERT INTO media.recordings (key_id, consent_ref, created_at, destroy_by) VALUES ('k1', 'C-1', $1, $2)"
	for _, tc := range []struct {
		created, destroyBy string
		ok                 bool
	}{
		{"2025-01-01 12:00:00+00", "2026-01-01", true},
		{"2025-01-01 12:00:00+00", "2026-01-02", false}, // 366 days, 12 months + 1 day
		{"2025-01-31 23:30:00+00", "2026-01-31", true},
		{"2025-01-31 23:30:00+00", "2026-02-01", false},
		{"2024-02-29 08:00:00+00", "2025-02-28", true},
		{"2024-02-29 08:00:00+00", "2025-03-01", false},
	} {
		_, err := owner.Exec(ctx, q, tc.created, tc.destroyBy)
		if tc.ok && err != nil {
			t.Errorf("created %s, destroy_by %s rejected: %v", tc.created, tc.destroyBy, err)
		}
		if !tc.ok && code(err) != "23514" {
			t.Errorf("created %s, destroy_by %s: err = %v, want 23514", tc.created, tc.destroyBy, err)
		}
	}
}

// CodeRabbit PR #3: timestamps are set by the database (clock_timestamp());
// no writer role may supply one and so backdate a withdrawal, an export or a
// recording's retention clock.
func TestWritersCannotSupplyDatabaseTimestamps(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := pgtest.Context(t)
	an := as(t, c, pg.StoreAnalytic, "an_writer")
	lk := as(t, c, pg.StoreLinkage, "lk_writer")
	md := as(t, c, pg.StoreMedia, "md_writer")
	for _, tc := range []struct {
		conn *pgx.Conn
		q    string
	}{
		{an, `INSERT INTO analytic.meds_records (study_id, parish, time_of_call, logic_version, zone_set_version,
			consent_status, exported_at) VALUES (gen_random_uuid(), 'Bwaise I', now(), 'tr-test+sha256:0123456789ab',
			'zs-1', 'consented', now() - interval '1 year')`},
		{an, "INSERT INTO analytic.suppression_ledger (study_id, reason, withdrawn_at) VALUES (gen_random_uuid(), 'withdrawal', now() - interval '1 year')"},
		{lk, "INSERT INTO linkage.pairs (study_id, incident_id, created_at) VALUES (gen_random_uuid(), gen_random_uuid(), now() - interval '1 year')"},
		{md, "INSERT INTO media.recordings (key_id, consent_ref, destroy_by, created_at) VALUES ('k1', 'C-1', current_date, now() + interval '5 years')"},
		{md, "INSERT INTO media.recordings (key_id, consent_ref, destroy_by, transcript_checked_at) VALUES ('k1', 'C-1', current_date, now())"},
	} {
		if _, err := tc.conn.Exec(ctx, tc.q); code(err) != "42501" {
			t.Errorf("%q: err = %v, want 42501", tc.q, err)
		}
	}
	// Without the timestamp columns the same writers still insert.
	for _, tc := range []struct {
		conn *pgx.Conn
		q    string
	}{
		{an, "INSERT INTO analytic.suppression_ledger (study_id, reason) VALUES (gen_random_uuid(), 'withdrawal')"},
		{lk, "INSERT INTO linkage.pairs (study_id, incident_id) VALUES (gen_random_uuid(), gen_random_uuid())"},
		{md, "INSERT INTO media.recordings (id, key_id, consent_ref, destroy_by) VALUES (gen_random_uuid(), 'k1', 'C-1', current_date)"},
	} {
		if _, err := tc.conn.Exec(ctx, tc.q); err != nil {
			t.Errorf("%q: %v", tc.q, err)
		}
	}
}

// CodeRabbit PR #3: destination, first action and 24–48 h status hold codes
// from closed lists, never free text such as a name, a phone number or an
// address (hard constraint 8).
func TestAnalyticCodedFieldsRejectFreeText(t *testing.T) {
	c := pgtest.Cluster(t)
	w := as(t, c, pg.StoreAnalytic, "an_writer")
	ctx := pgtest.Context(t)
	for _, col := range []string{"destination", "first_action_code", "status_24_48h"} {
		q := `INSERT INTO analytic.meds_records (study_id, parish, time_of_call, logic_version, zone_set_version,
			consent_status, ` + col + `) VALUES ($1, 'Bwaise I', now(), 'tr-test+sha256:0123456789ab', 'zs-1', 'consented', $2)`
		for _, bad := range []string{"Jane Doe +256700123456", "0772123456", "Mulago Hospital", "plot 4, kampala rd", "", "x" + strings.Repeat("y", 64)} {
			if _, err := w.Exec(ctx, q, uuid.NewV4(), bad); code(err) != "23514" {
				t.Errorf("%s = %q: err = %v, want 23514", col, bad, err)
			}
		}
		for _, good := range []any{"facility.mulago_nrh", "self-transport", "a", nil} {
			if _, err := w.Exec(ctx, q, uuid.NewV4(), good); err != nil {
				t.Errorf("%s = %v rejected: %v", col, good, err)
			}
		}
	}
}

func medsCount(ctx context.Context, t *testing.T, c *pgtest.TestCluster, id uuid.UUID) int {
	t.Helper()
	var n int
	if err := as(t, c, pg.StoreAnalytic, "an_reader").QueryRow(ctx,
		"SELECT count(*) FROM analytic.meds_records WHERE study_id = $1", id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// CodeRabbit PR #3: a withdrawal takes effect in the database itself. The
// suppression-ledger entry deletes the study ID's MEDS record, and the ID can
// never be inserted again, so a ledger replay after a restore re-applies it.
func TestSuppressionDeletesAndBlocksMEDSRecord(t *testing.T) {
	c := pgtest.Cluster(t)
	w := as(t, c, pg.StoreAnalytic, "an_writer")
	ctx := pgtest.Context(t)
	id, other := uuid.NewV4(), uuid.NewV4()
	for _, s := range []uuid.UUID{id, other} {
		if _, err := w.Exec(ctx, insertMEDS, s, "consented"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Exec(ctx, "INSERT INTO analytic.suppression_ledger (study_id, reason) VALUES ($1, 'withdrawal')", id); err != nil {
		t.Fatal(err)
	}
	if n := medsCount(ctx, t, c, id); n != 0 {
		t.Fatalf("withdrawn study ID still has %d MEDS record(s)", n)
	}
	if n := medsCount(ctx, t, c, other); n != 1 {
		t.Fatalf("suppression removed another study ID's record (count %d)", n)
	}
	if _, err := w.Exec(ctx, insertMEDS, id, "consented"); code(err) != "SA403" {
		t.Fatalf("re-insert of a suppressed study ID: err = %v, want SA403", err)
	}
	// A replayed ledger entry for an ID restored from backup deletes it again.
	owner := c.Owner(t, pg.StoreAnalytic)
	restored := uuid.NewV4()
	if _, err := w.Exec(ctx, insertMEDS, restored, "consented"); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, "INSERT INTO analytic.suppression_ledger (study_id, reason) VALUES ($1, 'dsr_deletion')", restored); err != nil {
		t.Fatal(err)
	}
	if n := medsCount(ctx, t, c, restored); n != 0 {
		t.Fatalf("replayed suppression left %d record(s)", n)
	}
}

// A MEDS insert that is still uncommitted when the withdrawal is recorded
// must not survive it: the two serialise on the study ID.
func TestSuppressionWinsOverConcurrentInsert(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := pgtest.Context(t)
	writer := as(t, c, pg.StoreAnalytic, "an_writer")
	withdrawer := as(t, c, pg.StoreAnalytic, "an_writer")
	id := uuid.NewV4()
	tx, err := writer.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		// A fresh bounded context: ctx may have expired by now.
		rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = tx.Rollback(rctx)
	}()
	if _, err := tx.Exec(ctx, insertMEDS, id, "consented"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := withdrawer.Exec(ctx, "INSERT INTO analytic.suppression_ledger (study_id, reason) VALUES ($1, 'withdrawal')", id)
		done <- err
	}()
	time.Sleep(300 * time.Millisecond) // let the withdrawal run (or block) first
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatalf("withdrawal did not finish: %v", ctx.Err())
	}
	if n := medsCount(ctx, t, c, id); n != 0 {
		t.Fatalf("MEDS record committed concurrently with its withdrawal survived (count %d)", n)
	}
}

// CodeRabbit PR #3: a NULL reason is a denied attempt, and it is logged like
// any other (granted = NULL used to fail the insert and lose the log row).
// The accessor must be a staff identity-provider subject (a UUID); free text
// is denied and never stored, nor is a reason outside the lawful list.
func TestLinkageDeniedAttemptsAreAlwaysLoggedWithoutFreeText(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := pgtest.Context(t)
	study, inc := uuid.NewV4(), uuid.NewV7()
	if _, err := as(t, c, pg.StoreLinkage, "lk_writer").Exec(ctx,
		"INSERT INTO linkage.pairs (study_id, incident_id) VALUES ($1, $2)", study, inc); err != nil {
		t.Fatal(err)
	}
	r := as(t, c, pg.StoreLinkage, "lk_reader")
	staff := uuid.NewV4().String()
	for _, tc := range []struct {
		reason, accessor any
	}{
		{nil, staff},
		{"withdrawal", "Jane Doe +256700123456"},
		{"withdrawal", nil},
		{"Jane Doe asked", staff},
	} {
		var got *uuid.UUID
		if err := r.QueryRow(ctx, "SELECT linkage.resolve_incident($1, $2, $3)", study, tc.reason, tc.accessor).Scan(&got); err != nil {
			t.Fatalf("resolve(reason=%v, accessor=%v): %v", tc.reason, tc.accessor, err)
		}
		if got != nil {
			t.Fatalf("resolve(reason=%v, accessor=%v) granted %v", tc.reason, tc.accessor, got)
		}
	}
	var total, granted, leaked int
	if err := c.Owner(t, pg.StoreLinkage).QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE granted), count(*) FILTER (WHERE l::text ~ 'Jane|256700')
		FROM linkage.access_log l WHERE study_id = $1`, study).Scan(&total, &granted, &leaked); err != nil {
		t.Fatal(err)
	}
	if total != 4 || granted != 0 || leaked != 0 {
		t.Fatalf("access_log total=%d granted=%d with free text=%d, want 4, 0, 0", total, granted, leaked)
	}
}

// CodeRabbit PR #3: the access log is append-only against TRUNCATE too.
func TestLinkageAccessLogCannotBeTruncated(t *testing.T) {
	c := pgtest.Cluster(t)
	if _, err := c.Owner(t, pg.StoreLinkage).Exec(pgtest.Context(t), "TRUNCATE linkage.access_log"); code(err) != "SA403" {
		t.Fatalf("owner TRUNCATE access_log: err = %v, want SA403", err)
	}
}

var schemaOf = map[pg.Store]string{
	pg.StoreOperational: "op", pg.StoreAudit: "audit", pg.StoreAnalytic: "analytic",
	pg.StoreLinkage: "linkage", pg.StoreMedia: "media",
}

// Review finding I2: a function a future migration adds must not be callable
// by any runtime role unless granted. PostgreSQL gives functions EXECUTE to
// PUBLIC by default; the owner's default privileges must revoke it.
func TestNewFunctionsAreNotExecutableByRuntimeRoles(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := pgtest.Context(t)
	for _, s := range pg.Stores() {
		owner := c.Owner(t, s)
		fn := schemaOf[s] + ".probe_new_function()"
		if _, err := owner.Exec(ctx, "CREATE FUNCTION "+fn+" RETURNS int LANGUAGE sql SECURITY DEFINER AS 'SELECT 1'"); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		for _, role := range s.RuntimeRoles() {
			var can bool
			if err := owner.QueryRow(ctx, "SELECT has_function_privilege($1, $2, 'EXECUTE')", role, fn).Scan(&can); err != nil {
				t.Fatal(err)
			}
			if can {
				t.Errorf("%s: runtime role %s can execute a newly created function", s, role)
			}
		}
	}
}

// Every function shipped by the migrations carries an explicit ACL with no
// PUBLIC grant.
func TestEveryStoreFunctionHasExplicitACL(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := pgtest.Context(t)
	for _, s := range pg.Stores() {
		rows, err := c.Owner(t, s).Query(ctx, `
			SELECT p.proname FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname = $1
			  AND (p.proacl IS NULL OR EXISTS (SELECT 1 FROM aclexplode(p.proacl) a WHERE a.grantee = 0))`, schemaOf[s])
		if err != nil {
			t.Fatal(err)
		}
		open, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		if len(open) > 0 {
			t.Errorf("%s: functions executable by PUBLIC: %v", s, open)
		}
	}
}

// CodeRabbit PR #3 (review of ead62cf), finding 2: op.ussd_steps.created_at
// is set by the database (clock_timestamp()). op_intake may insert only the
// data columns, so it cannot backdate or future-date a cached reply.
//
// The data columns differ along the migration history (0008 rekeys steps by
// a keyed MAC), so they are read from the catalogue. A zero-row INSERT …
// SELECT checks the privileges without needing a valid row.
func TestIntakeCannotSupplyUSSDStepTimestamp(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := pgtest.Context(t)
	owner := c.Owner(t, pg.StoreOperational)
	rows, err := owner.Query(ctx, `
		SELECT quote_ident(attname) FROM pg_attribute
		WHERE attrelid = 'op.ussd_steps'::regclass AND attnum > 0 AND NOT attisdropped AND attname <> 'created_at'
		ORDER BY attnum`)
	if err != nil {
		t.Fatal(err)
	}
	cols, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(cols) == 0 {
		t.Fatalf("op.ussd_steps data columns = %v, %v", cols, err)
	}
	var tableWide bool
	if err := owner.QueryRow(ctx, "SELECT has_table_privilege('op_intake', 'op.ussd_steps', 'INSERT')").Scan(&tableWide); err != nil {
		t.Fatal(err)
	}
	if tableWide {
		t.Error("op_intake holds table-wide INSERT on op.ussd_steps, which covers created_at")
	}
	intake := as(t, c, pg.StoreOperational, "op_intake")
	data := strings.Join(cols, ", ")
	if _, err := intake.Exec(ctx, "INSERT INTO op.ussd_steps ("+data+") SELECT "+data+" FROM op.ussd_steps WHERE false"); err != nil {
		t.Errorf("op_intake insert of the data columns: %v", err)
	}
	if _, err := intake.Exec(ctx, "INSERT INTO op.ussd_steps ("+data+", created_at) SELECT "+data+", created_at FROM op.ussd_steps WHERE false"); code(err) != "42501" {
		t.Errorf("op_intake insert supplying created_at: err = %v, want 42501", err)
	}
}
