# How to run the M0 milestone demo

**Type:** How-to (Diátaxis) · **Code:** `internal/platform/demo/m0.go` · **Spec:** §12.2

**Without a PostgreSQL server** (embedded PostgreSQL 18.6):

```bash
go test -tags integration -count=1 -v -run TestM0FixtureIncident ./tests/e2e/
```

**Against a development server** (it creates and then drops isolated `sauti_*_demo<random>` databases):

```bash
go run ./cmd/sautictl demo m0 --admin-dsn "postgres://postgres:…@127.0.0.1:5432/postgres?sslmode=disable"
```

Add `--keep` to keep the databases for inspection.

**What a passing run proves:**

| Step | Proof |
|---|---|
| 1 | A simulated USSD dial opens the incident; the reply carries a Damm-valid 3-3-4 reference; T1 comes from the database clock |
| 2 | Fixture events (classification, T2…T7, closure) pass the single write path |
| 3 | outbox-relay delivers every outbox row to the audit ledger |
| 4 | One ledger entry per event, and no phone number anywhere in the ledger |
| 5 | An ML-DSA-65 checkpoint is signed and written to the write-once witness |
| 6 | `verify-ledger` and `verify-projections` pass |
| 7 | Flipping one byte of entry 3 as a superuser makes verification fail at sequence 3 |
| 8 | Retrying the first callback returns the identical screen and creates nothing |
