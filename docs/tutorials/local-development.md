# Tutorial: run Sauti Dispatch locally

**Type:** Tutorial (Diátaxis) · About 15 minutes · Linux x86-64

By the end you will have run the test suites against a real PostgreSQL 18 and watched the M0 demo move one incident from a simulated phone into a verified, tamper-evident audit ledger.

## 1. Prerequisites

- Go: any version with `GOTOOLCHAIN=auto`. The module pins `toolchain go1.27.4`, and Go downloads it on first use.
- `curl` and `tar`, to fetch the pinned tools.
- Python 3.11 or newer, for the governance gates.
- No Docker is needed: each integration test process starts a private embedded PostgreSQL 18.6 with random credentials. The first run downloads it, about 30 MB.
- To use a shared server instead, set both `SAUTI_TEST_PG_DSN` and `SAUTI_TEST_ROLE_PASSWORD`.

## 2. Unit tests

```bash
scripts/ci/unit.sh
```

Without a C compiler the race detector is skipped. CI runs it.

## 3. Integration tests and the M0 demo

```bash
go test -tags integration -count=1 -v ./tests/e2e/
```

You should see eight `✓ step` lines: the dial, the events, the relay, a ledger with no phone numbers, a signed checkpoint, verification, a detected tamper, and an idempotent retry. Then:

```bash
scripts/ci/integration.sh      # every integration test
```

## 4. The quality gates CI runs

```bash
scripts/ci/fmt.sh && scripts/ci/arch.sh && scripts/ci/sqlc-diff.sh
python3 -m venv .venv && .venv/bin/pip install --require-hashes --no-deps -r governance/requirements.txt
PYTHON=.venv/bin/python AMENDGATE_BASE=main scripts/ci/governance.sh
```

## 5. Run the processes against a development server (optional)

With a disposable PostgreSQL 18 superuser URL in `SAUTI_TEST_PG_DSN`:

```bash
export SAUTI_DEV_DB_PASSWORD=change-me-dev-only
go run ./cmd/sautictl dev-bootstrap --admin-dsn "$SAUTI_TEST_PG_DSN" --migrate
go run ./cmd/sautictl dev-keygen --dir .devkeys
cp .env.example .env   # adjust, then: set -a; . ./.env; set +a
go run ./cmd/intake-gw &
go run ./cmd/outbox-relay &
```

Next, read `docs/explanation/architecture.md`, then the ADR index.
