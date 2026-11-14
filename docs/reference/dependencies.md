# Dependencies

**Type:** Reference (Diátaxis) · **Policy:** [ADR-0015](../adr/ADR-0015-dependency-and-supply-chain-policy.md) · Verified 2026-11-12

Every dependency needs a note here before it merges, covering why it is needed, its licence, maintainer health and the fallback (Build Prompt §17).

## Shipped (runtime)

| Module | Version | Licence | Why | Health | Fallback |
|---|---|---|---|---|---|
| `github.com/jackc/pgx/v5` | v5.13.0 | MIT | PostgreSQL driver and pool (stack choice) | very active; ≥ 5.12.0 required (two GHSAs fixed) | `database/sql` + lib/pq (degraded) |
| `go.yaml.in/yaml/v4` | v4.0.0 | Apache-2.0 / MIT | strict YAML decoding of versioned configuration and (M1) triage tables | maintained by the YAML organisation (successor of go-yaml) | JSON configuration via `encoding/json` |
| indirect: `jackc/pgpassfile`, `jackc/pgservicefile`, `jackc/puddle/v2`, `golang.org/x/text`, `golang.org/x/sync` | pinned in `go.sum` | MIT / BSD-3-Clause | pgx internals | — | — |
| `cel.dev/cel-go` | v0.33.2 (2026-10-30) | Apache-2.0 | conditions of the signed triage decision table (ADR-0019); the expression language Kubernetes uses for admission policy | Google-maintained, monthly releases; module path moved from `github.com/google/cel-go` | hand-written Go `switch` behind the same `triage.Engine` interface (loses clinician-editable rules) |
| `github.com/coder/websocket` | v1.8.15 (2026-06-15) | ISC | the ARI event stream: Go's standard library has no WebSocket client (ADR-0023) | maintained by Coder; no dependencies | `golang.org/x/net/websocket` (deprecated) behind the same `ari.Client` |
| indirect (cel-go): `cel.dev/expr`, `github.com/antlr4-go/antlr/v4`, `google.golang.org/protobuf`, `google.golang.org/genproto/googleapis/{api,rpc}`, `golang.org/x/exp`, `go.yaml.in/yaml/v3` | pinned in `go.sum` | Apache-2.0 / BSD-3-Clause / MIT | CEL parser, checker and value types | — | — |

The standard library covers UUIDs (`uuid`, Go 1.27), ML-DSA (`crypto/mldsa`), HKDF, AES-GCM, HTTP, logging and metrics.

## Test only (never shipped)

| Module | Version | Licence | Why |
|---|---|---|---|
| `github.com/fergusstrange/embedded-postgres/v2` | v2.0.1 | MIT | PostgreSQL 18.6 for integration tests without a container runtime (ADR-0012) |

## Build and scan tools (pinned, not linked)

| Tool | Version | Pinning |
|---|---|---|
| Go toolchain | go1.27.4 | `toolchain` directive in `go.mod` |
| sqlc | v1.31.1 | release binary sha256 in `scripts/tools/install-sqlc.sh` |
| golangci-lint | v2.15.0 | GitHub Action pinned by commit SHA; local binary via `scripts/tools/fetch.sh` |
| gitleaks / osv-scanner | v8.30.1 / v2.7.1 | release binaries sha256 in `scripts/tools/install-scanners.sh` |
| promtool (alert rule checks and tests) | v3.13.5, Prometheus 3.13 LTS (supported to 2027-07-31) | release tarball sha256, from the release's `sha256sums.txt`, in `scripts/tools/install-promtool.sh` (verified 2026-11-14) |
| govulncheck / cyclonedx-gomod | v1.9.0 / v1.12.0 | `build/tools.mod` (`go tool -modfile=build/tools.mod …`) |
| PyYAML (governance gates) | 6.0.3 | `governance/requirements.txt` with `--require-hashes` |
| GitHub Actions | checkout v7.0.1, setup-go v7.0.0, setup-python v7.1.0, upload-artifact v7.0.2, golangci-lint-action v9.3.0 | full commit SHAs in `.github/workflows/ci.yml` |
| PostgreSQL CI image | postgres:18.6 | digest in `.github/workflows/ci.yml` |
