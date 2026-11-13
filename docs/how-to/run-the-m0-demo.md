# How to run the M0 milestone demo

**Type:** How-to (Diátaxis) · **Code:** `internal/platform/demo/m0.go` · **Spec:** §12.2

**Without a PostgreSQL server** (embedded PostgreSQL 18.6):

```bash
go test -tags integration -count=1 -v -run TestM0FixtureIncident ./tests/e2e/
```

**Against a development server** (it creates and then drops isolated `sauti_*_demo<random>` databases):

```bash
export SAUTI_ENV=dev             # the demo refuses production before connecting
export SAUTI_DEV_DB_PASSWORD=…   # the dev server's role password (the one dev-bootstrap used)
go run ./cmd/sautictl demo m0 --admin-dsn "postgres://postgres:…@127.0.0.1:5432/postgres?sslmode=disable"
```

The demo reuses the server's role password and never changes it, because roles are shared by everything else on that server.

Add `--keep` to keep the databases for inspection. The run's work directory stays with them, because the kept ciphertext needs its development keys and the kept ledger its checkpoint witness; the demo prints its path (`kept: development keys and checkpoint witness in …`). Delete the directory and drop the `_demo…` databases yourself when done.

**What a passing run proves:**

| Step | Proof |
|---|---|
| 1 | A simulated caller completes Tool 1 on USSD (placeholder wording): the incident opens on the first callback with T1 from the database clock, is classified red by the signed triage table after screen 5, gets T2 after screen 7, and the closing screen carries the Damm-valid 3-3-4 reference |
| 2 | Fixture events (classification, T2…T7, closure) pass the single write path |
| 3 | outbox-relay delivers every outbox row to the audit ledger |
| 4 | One ledger entry per event, and no phone number anywhere in the ledger |
| 5 | An ML-DSA-65 checkpoint is signed and written to the write-once witness |
| 6 | `verify-ledger` and `verify-projections` pass |
| 7 | Flipping one byte of entry 3 as a superuser makes verification fail at sequence 3 |
| 7b | Rewriting entry 3, recomputing the chain with the ledger's own canonical function and deleting the database checkpoints fools chain-only verification. The write-once witness still fails it, and the anchored checkpointer refuses to sign. |
| 8 | Retrying the first callback returns the identical screen and creates nothing |

The run is bounded to 10 minutes and each cleanup step to 30 seconds, so a held lock or a stalled server fails the demo instead of hanging it.
