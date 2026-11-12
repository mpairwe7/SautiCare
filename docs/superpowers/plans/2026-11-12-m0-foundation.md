# M0 Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the foundation that a fixture incident runs through end to end. The demo:
1. A simulated caller dials.
2. The incident is created with a Damm-checked reference and a database-set T1.
3. Events are appended through the single write path.
4. The outbox relays them into the hash-chained audit ledger.
5. A checkpoint is signed with ML-DSA-65 and witnessed.
6. `verify-ledger` and `verify-projections` pass, and tampering is detected.

**Architecture:** A hexagonal modular monolith. Bounded contexts live in `internal/<context>`: pure domain code plus application services, with ports as interfaces. Adapters live in `internal/platform/*`, and `cmd/*` are composition roots. The PostgreSQL functions `op.open_incident` and `op.append_incident_event` are the only write path. `outbox-relay` is the only process with audit credentials.

**Tech Stack:**
- Go 1.27.4: stdlib `uuid`, `crypto/mldsa`, `crypto/hkdf`, `log/slog`.
- pgx v5.13.0 plus sqlc v1.31.1 (pinned release binary).
- PostgreSQL 18.6: embedded-postgres v2.0.1 locally, an official image pinned by digest in CI.
- `go.yaml.in/yaml/v4` v4.0.0.
- Python 3.11 with PyYAML (hash-pinned) for the governance gates.

**Spec:** `docs/superpowers/specs/2026-11-12-sauti-dispatch-design.md` (read §4–§12 first). **Roadmap:** `docs/superpowers/plans/2026-11-12-phased-roadmap.md`.

## Global Constraints

- **Module and toolchain:** module path `sautihealth.com/sauti`; `go 1.27`; `toolchain go1.27.4`.
- **Dependencies:** the runtime set is limited to `github.com/jackc/pgx/v5` (≥ v5.12.0), `go.yaml.in/yaml/v4`, and the standard library. Test-only: `github.com/fergusstrange/embedded-postgres/v2`. Anything else needs a dependency note in `docs/reference/dependencies.md`.
- **No cgo:** `CGO_ENABLED=0` locally. `-race` runs only in CI.
- **Timestamps:** every T-timestamp is set by the database (`clock_timestamp()`). Go never supplies T-times.
- **Logs and audit:** logs, errors, audit entries and outbox payloads never contain a phone number or a name (hard constraints 5, 8; ISP §5.6).
- **Incident reference:** 9 random digits from `crypto/rand` plus a Damm check digit. Canonical form is 10 digits; spoken form is `ddd ddd dddd`.
- **Config values:** resume window `10m`; dispatch trigger screen `7`; screen character limit `160` by default; checkpoint interval `5m`. Feature flags `android_app`, `caller_link`, `speech_assist` and `app_api` are all `false`. `study_window_active: false`. `protocol_status: pending`.
- **Production guards:**
  - Each binary refuses to start with `SAUTI_ENV=production` if `devkeys` is configured or if `protocol_status` is `pending`.
  - `sautictl dev-bootstrap` refuses `SAUTI_ENV=production`.
- **Custom SQLSTATEs:**
  - `SA404`: unknown incident.
  - `SA409`: version conflict.
  - `SA422`: illegal transition or severity lowered.
- **Tests:**
  - Integration tests carry `//go:build integration` and read `SAUTI_TEST_PG_DSN` (an admin DSN); without it, they start embedded-postgres.
  - Test names below are exact.
- **Commits:** one commit per task, ending with the `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` trailer.

## Review Focus

These are inputs that no task's happy-path tests would exercise. Each is pinned by a test in its owning task.

1. **Duplicate first callback.** Africa's Talking delivers the first callback twice, concurrently. Expect one incident and one session, and both responses carry the same reference. Test: Task 11, `TestConcurrentFirstCallbacksCreateOneIncident`.
2. **Phone numbers in error paths.** A phone number reaches an error or log message, for example a pgx error that echoes a parameter. Expect the redaction handler to mask it. Test: Task 2, `TestRedactionMasksPhoneInsideErrorValues`.
3. **Event payload containing a phone-like string.** Expect the audit entry to carry only the payload's SHA-256, never its content. Test: Task 10, `TestAuditEntryNeverCarriesPayloadContent`.
4. **Audit store down for several minutes.** Expect the outbox to keep growing, intake to keep answering, and the relay to catch up with no gap or duplicate in `seq`. Test: Task 10, `TestRelayCatchesUpAfterLedgerOutage`.
5. **No partition for the month yet.** An event arrives for a month whose partition hasn't been created. Expect the insert to succeed (via the default partition) and `op.default_partition_rows()` to report a value above 0, so the alert fires. Test: Task 7, `TestEventLandsInDefaultPartitionAndIsReported`.

---

### Task 1: Module bootstrap and incident reference (Damm)

**Files:**
- Create: `go.mod`, `internal/incident/damm.go`, `internal/incident/reference.go`, `scripts/ci/unit.sh`
- Test: `internal/incident/damm_test.go`, `internal/incident/reference_test.go`

**Interfaces:**
- Produces:
  - `func DammCheckDigit(digits string) (byte, error)` returns the ASCII digit.
  - `func DammValid(digits string) bool`.
  - `type Reference string` holds the canonical 10 digits.
  - `func NewReference(r io.Reader) (Reference, error)`.
  - `func ParseReference(s string) (Reference, error)` accepts spaces and hyphens.
  - `func (Reference) Spoken() string` gives `"123 456 7890"`; `func (Reference) Last4() string`.
  - `var ErrInvalidReference`.

- [ ] **Step 1:** `go mod init sautihealth.com/sauti`. Set `go 1.27` and `toolchain go1.27.4`. Write `scripts/ci/unit.sh`: `set -euo pipefail; go test -shuffle=on -count=1 ./...`, plus `-race` when `CGO_ENABLED=1`.
- [ ] **Step 2: Write failing tests.** Test vectors and properties:
  - `TestDammWikipediaVector`: `DammCheckDigit("572") == '4'` and `DammValid("5724")`.
  - `TestDammDetectsEverySingleDigitError`: for 2,000 random 9-digit bodies, every single-position substitution of the 10-digit reference fails `DammValid`.
  - `TestDammDetectsEveryAdjacentTransposition`: every swap of unequal neighbours fails.
  - `TestNewReferenceIsTenDigitsAndValid`.
  - `TestParseReferenceAcceptsSpokenForms`: `"123 456 7890"` and `"123-456-7890"` both parse to the same reference, built from a valid body.
  - `TestParseReferenceRejects`: wrong check digit, 9 or 11 digits, letters.
  - `FuzzParseReference`: never panics; any accepted value round-trips through `Spoken()`.
- [ ] **Step 3:** Run `go test ./internal/incident/...`. Expected: FAIL (undefined).
- [ ] **Step 4: Implement.** Use the standard Damm table (spec research, Wikipedia): rows are the interim digit, columns the next digit, starting from interim 0. `NewReference` draws 9 digits by rejection sampling bytes < 250 from `r`, taking value mod 10.
- [ ] **Step 5:** Run `go test ./internal/incident/...`. Expected: PASS. Then commit: `feat(incident): Damm-checked 3-3-4 incident reference`.

### Task 2: PII types, log redaction, egress allowlist

**Files:**
- Create: `internal/pii/pii.go`, `internal/platform/logging/redact.go`, `internal/platform/egress/egress.go`
- Test: `internal/pii/pii_test.go`, `internal/platform/logging/redact_test.go`, `internal/platform/egress/egress_test.go`

**Interfaces:**
- Produces:
  - `type Phone struct{ e164 string }`. `func ParsePhone(s string) (Phone, error)` normalises `07XXXXXXXX`, `2567…` and `+256 7…` to `+2567…` and accepts any other E.164 number starting with `+`. `func (Phone) E164() string` is the only way to get the digits. `String()`, `GoString()`, `LogValue()` and `MarshalJSON()` all render `"+256•••••••••"` (country code plus bullets). `func (Phone) IsZero() bool`.
  - `type PersonName struct{ v string }`: the same masking (`"[name]"`) and `Reveal() string`.
  - `func logging.NewHandler(w io.Writer, level slog.Level) slog.Handler`: JSON. Its `ReplaceAttr` replaces any string value, or `error` text, that matches `\+?\d[\d\s-]{8,14}\d` with `"[redacted-number]"`, and the values of keys `phone`, `msisdn`, `phoneNumber`, `caller`, `name` with `"[redacted]"`.
  - `func egress.NewClient(allow []string, timeout time.Duration) *http.Client`; `var egress.ErrDenied`.

- [ ] **Step 1: Write failing tests:**
  - `TestPhoneMasksEverywhere`: `fmt.Sprint`, `%v`, `%#v`, `json.Marshal` and slog output of `ParsePhone("0772123456")` contain no `772123456`.
  - `TestParsePhoneUganda`: the three local forms give `"+256772123456"`; rejects `"12"` and `"abc"`.
  - `TestRedactionMasksPhoneInsideErrorValues`: `slog.Error("x", "err", fmt.Errorf("pgx: value 0772123456 bad"))` gives output without the digits.
  - `TestRedactionMasksDeniedKeys`.
  - `TestEgressDeniesUnlistedHost`: `errors.Is(err, egress.ErrDenied)` against an `httptest` server whose host is not allowlisted.
  - `TestEgressAllowsListedHost`.
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3: Implement.** The egress check happens in a custom `RoundTripper` on `req.URL.Hostname()`, before dialling.
- [ ] **Step 4:** Run the tests. Expected: PASS. Then commit: `feat(pii,logging,egress): masked PII types, redaction, egress allowlist`.

### Task 3: Crypto: envelope, MAC lookup, ML-DSA signer, dev keys, witness

**Files:**
- Create: `internal/crypto/{envelope.go,mac.go,sign.go,ports.go,identifiers.go}`, `internal/platform/devkeys/{devkeys.go,witness.go}`
- Test: `internal/crypto/*_test.go`, `internal/platform/devkeys/devkeys_test.go`

**Interfaces:**
- Produces, in `crypto`:
  - `type DataKey struct{ ID string; Key []byte }`.
  - `type KeyProvider interface{ Current(ctx context.Context, class string) (DataKey, error); Previous(ctx context.Context, class string) (DataKey, bool, error); ByID(ctx context.Context, id string) (DataKey, error) }`.
  - `type AAD struct{ Store, Table, Column, RowID string }` and `func (AAD) Bytes() []byte`, which gives `"sauti-aad/v1"` followed by each field as a 4-byte big-endian length plus its bytes.
  - `type Sealed struct{ KeyID string; Ciphertext []byte }`, with ciphertext laid out as nonce(12) ‖ ct ‖ tag.
  - `func NewEnvelope(k KeyProvider, rand io.Reader) *Envelope`.
  - `func (*Envelope) Seal(ctx, class string, pt []byte, aad AAD) (Sealed, error)` and `func (*Envelope) Open(ctx, s Sealed, aad AAD) ([]byte, error)`.
  - `type MAC struct{ KeyID string; Sum []byte }`.
  - `func (*Envelope) MAC(ctx, class string, msg []byte) (MAC, error)`. The HMAC-SHA-256 key is `hkdf.Key(sha256.New, dataKey, nil, "sauti/mac/v1", 32)`.
  - `func (*Envelope) LookupMACs(ctx, class string, msg []byte) ([]MAC, error)` covers the current and previous periods.
  - `type Signer interface{ KeyID() string; PublicKey() []byte; Sign(ctx context.Context, msg []byte) ([]byte, error) }`.
  - `func VerifyMLDSA65(pub, msg, sig []byte) error`, using context `"sauti-audit-checkpoint/v1"`.
  - `type Witness interface{ Put(ctx context.Context, name string, data []byte) error }`.
  - `const ClassIdentifiers = "operational.identifiers"`.
  - `type Identifiers struct{ Env *Envelope }`, with `SealPhone(ctx, pii.Phone, AAD) (Sealed, error)` and `PhoneMAC(ctx, pii.Phone) (MAC, error)`.
- Produces, in `devkeys`:
  - `func Generate(dir string, rand io.Reader) error` writes `keys.json` (0600) holding the current and previous data keys and an ML-DSA-65 seed.
  - `func Open(dir, env string) (*Store, error)` returns `ErrProductionForbidden` when `env == "production"`.
  - `*Store` implements `crypto.KeyProvider`, and `(*Store) Signer() crypto.Signer`.
  - `func NewFSWitness(dir string) *FSWitness`, a write-once witness (`O_EXCL`, file mode 0444).

- [ ] **Step 1: Write failing tests:**
  - `TestSealOpenRoundTrip`.
  - `TestOpenFailsWithSwappedAAD`: a different `RowID` makes `Open` fail.
  - `TestOpenFailsWithUnknownKeyID`.
  - `TestLookupMACsMatchesPreviousPeriod`: a MAC made with the previous key appears in `LookupMACs`.
  - `TestMLDSA65SignVerify`, `TestVerifyRejectsTamperedBody`.
  - `TestDevkeysRefuseProduction`.
  - `TestWitnessIsWriteOnce`: a second `Put` of the same name errors.
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3: Implement** with `crypto/aes`, `crypto/cipher` (GCM), `crypto/hmac`, `crypto/hkdf` and `crypto/mldsa` (`MLDSA65()`, `NewPrivateKey(params, seed)`, `Sign` with `&mldsa.Options{Context: …}`).
- [ ] **Step 4:** Run the tests. Expected: PASS. Then commit: `feat(crypto): AES-256-GCM envelope with row-bound AAD, HMAC lookup, ML-DSA-65, dev keys`.

### Task 4: Incident domain: events, states, transitions, reducer, commands, service

**Files:**
- Create: `internal/incident/{event.go,state.go,transitions.go,fold.go,command.go,service.go,errors.go}`
- Test: `internal/incident/{fold_test.go,service_test.go}`

**Interfaces:**
- Consumes: Task 1 `Reference`; Task 3 `crypto.Sealed`, `crypto.MAC`, `crypto.AAD`; Task 2 `pii.Phone`.
- Produces:
  - **States:** `type State string`, with constants `StateIntake="intake"`, `StateTriaged="triaged"`, `StateDispatching="dispatching"`, `StateAssigned="assigned"`, `StateOnScene="on_scene"`, `StateTransporting="transporting"`, `StateAtFacility="at_facility"`, `StateClosed="closed"`, `StateMerged="merged"`. `func (State) Terminal() bool` is true for closed and merged.
  - **Event types:** `type EventType string`, with constants:
    - `EvSessionStarted="session.started"` (T1)
    - `EvScreenAnswered="screen.answered"`
    - `EvClassified="incident.classified"`
    - `EvDispatchReady="incident.dispatch_ready"` (T2)
    - `EvAlertIssued="alert.issued"` (T3, first)
    - `EvAccepted="responder.accepted"` (T4, first)
    - `EvArrived="responder.arrived"` (T5, first)
    - `EvDeparted="responder.departed"` (T6)
    - `EvAtFacility="responder.at_facility"` (T7)
    - `EvFlagged="incident.flagged"`
    - `EvEscalated="incident.escalated"`
    - `EvMerged="incident.merged"`
    - `EvClosed="incident.closed"`
  - **Severity:** `type Severity string`, with `SevRed`, `SevYellow`, `SevGreen` and `func (Severity) Rank() int` (red 3 > yellow 2 > green 1).
  - **Transitions:** `type Transition struct{ From State; Event EventType; To State }`. `From == "*"` means any non-terminal state, and `To == ""` means the state is unchanged. `func Transitions() []Transition` is the exact table:
    - intake →classified→ triaged
    - triaged →dispatch_ready→ dispatching
    - dispatching →alert.issued→ dispatching
    - assigned →alert.issued→ assigned
    - dispatching →accepted→ assigned
    - assigned →accepted→ assigned
    - assigned →arrived→ on_scene
    - on_scene →arrived→ on_scene
    - on_scene →departed→ transporting
    - transporting →at_facility→ at_facility
    - the `*` rows: screen.answered, flagged, escalated, closed (→ closed)
    - intake, triaged and dispatching →merged→ merged
  - **Event:** `type Event struct{ ID, IncidentID uuid.UUID; Seq int64; Type EventType; RecordedAt time.Time; ActorKind string; StaffID *uuid.UUID; Payload json.RawMessage; IdemKey, LogicVersion string }`.
  - **Projection:** `type Projection struct{ ID uuid.UUID; Reference Reference; State State; Version int64; Channel string; Severity Severity; LogicVersion string; T [8]*time.Time; Escalated bool; Flags []string; MergedInto *uuid.UUID }`. `T[1]..T[7]` are T1–T7.
  - **Reducer:** `func Apply(p Projection, e Event) (Projection, error)` and `func Fold(events []Event) (Projection, error)`. The first event must be `EvSessionStarted`, whose payload `{"reference":…,"channel":…}` sets Reference and Channel. `EvClassified` reads `{"severity":…,"logic_version":…}` and returns `ErrSeverityLowered` on a downgrade. `EvFlagged` reads `{"flag":…}`. `EvMerged` reads `{"into":…}`. T-times use first-wins (the earliest event).
  - **Errors:** `ErrIllegalTransition`, `ErrSeverityLowered`, `ErrEmptyHistory`, `ErrVersionConflict`, `ErrNotFound`, `ErrReferenceTaken`.
  - **Commands:** `type Channel string` (`ChannelUSSDAT="ussd_at"`, `ChannelUSSDUCC="ussd_ucc"`, `ChannelIVR="ivr"`). The 10 command structs (spec §4.4) live in `command.go`; `StartSession{Channel Channel; ProviderSessionID, ServiceCode, NetworkCode string; Phone pii.Phone; IdemKey string}` is the only one used in M0.
  - **Ports:**
    - `type Store interface{ OpenIncident(ctx, OpenParams) (Opened, error); Append(ctx, AppendParams) (Appended, error); Events(ctx, id uuid.UUID) ([]Event, error) }`.
    - `type OpenParams struct{ Reference Reference; Channel Channel; SessionID uuid.UUID; ProviderSessionID, NetworkCode, IdemKey string; Phone crypto.Sealed; PhoneMAC crypto.MAC }`.
    - `type Opened struct{ IncidentID uuid.UUID; Reference Reference; EventID uuid.UUID; T1 time.Time; Created bool }`.
    - `type AppendParams struct{ IncidentID uuid.UUID; ExpectedVersion int64; Type EventType; Payload json.RawMessage; ActorKind string; StaffID *uuid.UUID; IdemKey, LogicVersion string }`. `ExpectedVersion == 0` means "any".
    - `type Appended struct{ Event Event; NewState State; Duplicate bool }`.
    - `type PhoneSealer interface{ SealPhone(ctx context.Context, p pii.Phone, aad crypto.AAD) (crypto.Sealed, error); PhoneMAC(ctx context.Context, p pii.Phone) (crypto.MAC, error) }`.
  - **Service:** `func NewService(s Store, ps PhoneSealer, rand io.Reader) *Service`.
    - `(*Service) StartSession(ctx, StartSession) (Opened, error)`: generates the session ID with `uuid.NewV7()`; seals the phone with `AAD{"operational","sessions","phone",sessionID}`; retries `ErrReferenceTaken` up to 5 times with a new reference.
    - `(*Service) Append(ctx, AppendParams) (Appended, error)`: rejects an unknown `Type` with `ErrIllegalTransition` before reaching the store.

- [ ] **Step 1: Write failing tests:**
  - `TestFoldFixtureLifecycle`: session → classified red → dispatch_ready → alert → accepted → arrived → departed → at_facility → closed. Asserts the State at each step, T1..T7 set from the respective events, and Version = 9.
  - `TestApplyRejectsIllegalTransition`: departed in triaged.
  - `TestApplyRejectsSeverityLowering`: red then green.
  - `TestTerminalStatesRejectStarEvents`.
  - `TestFoldIncrementalEqualsBatch`: applying events one by one equals `Fold`.
  - `TestTransitionsCoverEveryEventType`.
  - `TestStartSessionRetriesReferenceCollision`: a fake store returns `ErrReferenceTaken` twice, and the third reference succeeds.
  - `TestStartSessionSealsPhoneWithSessionAAD`: a fake sealer records the AAD.
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the tests. Expected: PASS. Then commit: `feat(incident): event-sourced aggregate, transition table, reducer, service`.

### Task 5: Versioned config, fingerprint, amendment gate

**Files:**
- Create:
  - `internal/config/{config.go,load.go}`
  - `config/study.yaml`, `config/platform.yaml`
  - `config/gated/{intake.yaml,dispatch.yaml,flags.yaml,screens.yaml}`
  - `tools/amendgate/{main.go,check.go}`
  - `governance/{amendments.yaml,gated-paths.txt}`
- Test: `internal/config/config_test.go`, `tools/amendgate/check_test.go`

**Interfaces:**
- Produces:
  - `type Bundle struct{ ProtocolStatus string; Study Study; Intake Intake; Dispatch Dispatch; Flags Flags; Screens Screens; Platform Platform; Fingerprint string }`.
  - `type Duration struct{ time.Duration }`, unmarshalled from a string like `"10m"`.
  - `Intake{ ResumeWindow Duration; DispatchTriggerScreen int; DefaultScreenCharLimit int; ScreenCharLimits map[string]int }`.
  - `Dispatch{ RingStepMetres, MaxRadiusMetres, RedParallelCount int; RingInterval, CascadeTimeout, ForwardAckTimeout, VoiceFallbackAfter Duration; RankingOrder []string; RankingVersion, Mode string }`. Values are those of Build Prompt §10.4, with `Mode: "protocol"` (ring 1 only until the amendment).
  - `Flags{ AndroidApp, CallerLink, SpeechAssist, AppAPI bool }`.
  - `Screens{ ProtocolStatus string; StartedTestMode, InternalError map[string]string }`, keyed by `en` and `lg`. Luganda values are `"PENDING-TRANSLATION"` until the signed-off copy arrives.
  - `Platform{ CheckpointInterval Duration; OutboxBatchSize int; RelayPollInterval Duration }`.
  - `func Load(fsys fs.FS) (Bundle, error)` decodes strictly with `KnownFields(true)`.
  - `Fingerprint` is `"sha256:" + hex(sha256(concat over sorted gated paths of path‖0x00‖sha256(content)))`.
  - `func (Bundle) ProductionReady() error`.
  - In `amendgate`:
    - `type ChangedFile struct{ Path, SHA256 string; Deleted bool }`
    - `type Amendment struct{ Reference, ApprovedOn string; Files []AmendedFile }`
    - `type AmendedFile struct{ Path, SHA256 string }`
    - `type Violation struct{ Path, Reason string }`
    - `func Check(windowActive bool, gatedGlobs []string, changed []ChangedFile, ams []Amendment) []Violation`

- [ ] **Step 1: Write failing tests:**
  - `TestLoadRepoConfigDefaults`: `ResumeWindow == 10*time.Minute`, `DispatchTriggerScreen == 7`, `CascadeTimeout == 90s`, `RedParallelCount == 3`, all flags false, and `ProtocolStatus == "pending"`.
  - `TestLoadRejectsUnknownField`.
  - `TestFingerprintStableAndSensitive`.
  - `TestProductionReadyFailsWhilePending`.
  - In amendgate:
    - `TestCheckPassesWhenWindowInactive`
    - `TestCheckFailsGatedChangeWithoutAmendment`
    - `TestCheckPassesWithMatchingAmendmentHash`
    - `TestCheckFailsWhenHashDiffersFromAmendment`
    - `TestCheckIgnoresUngatedPaths`
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3: Implement.**
  - Gated globs are listed in `governance/gated-paths.txt`: `config/gated/**`, `triage-rules/**`, `config/study.yaml`.
  - The CLI finds changed files with `git diff --name-status <base>...HEAD`. The window is active if it is active at either base or head.
  - The CLI exits 1 and prints its violations if there are any.
- [ ] **Step 4:** Run the tests. Expected: PASS. Then commit: `feat(config): versioned config bundle, fingerprint, amendment gate`.

### Task 6: Migrator, dev bootstrap, PostgreSQL test harness

**Files:**
- Create:
  - `internal/platform/pg/migrate/migrate.go`
  - `internal/platform/pg/bootstrap/bootstrap.go`
  - `internal/platform/pg/pool.go`
  - `internal/platform/pg/pgtest/pgtest.go`
  - `db/{operational,audit,analytic,linkage,media}/embed.go`
- Test: `internal/platform/pg/migrate/migrate_integration_test.go`, `internal/platform/pg/bootstrap/bootstrap_integration_test.go`

**Interfaces:**
- Produces:
  - `type Store string` with `StoreOperational="operational"`, `StoreAudit="audit"`, `StoreAnalytic="analytic"`, `StoreLinkage="linkage"`, `StoreMedia="media"`. `func Stores() []Store`.
  - Each `db/<store>` package exports `var Migrations embed.FS`, embedding `migrations/*.sql`.
  - `func migrate.Run(ctx context.Context, conn *pgx.Conn, store string, fsys fs.FS) ([]string, error)`:
    - Files are `NNNN_name.sql`.
    - Uses the table `public.schema_migrations(version text PRIMARY KEY, sha256 text NOT NULL, applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`.
    - Takes a session advisory lock on `hashtext('sauti-migrate:'||store)`.
    - Runs one transaction per file.
    - Returns `migrate.ErrChecksumMismatch` if an applied file's hash differs.
  - `type bootstrap.Options struct{ Suffix, Password, Env string }`.
  - `func bootstrap.Run(ctx context.Context, admin *pgx.Conn, o Options) (map[Store]string, error)`, which returns each database name.
    - Creates every role in spec §6.1 if missing: the owners `op_owner`, `audit_owner`, `an_owner`, `lk_owner`, `md_owner`; the operational runtime roles `op_intake`, `op_ivr`, `op_dispatch`, `op_console`, `op_payments`, `op_relay`, `op_pseudonymiser`, `op_retention`, `op_readonly`; and `audit_appender`, `audit_reader`, `an_writer`, `an_reader`, `lk_writer`, `lk_reader`, `md_writer`, `md_reader`. All have LOGIN and the password.
    - Creates `sauti_<store><suffix>`, owned by that store's owner.
    - Runs `REVOKE CONNECT … FROM PUBLIC` and grants CONNECT only to that store's roles.
    - Refuses when `Env == "production"`.
  - `func pg.Connect(ctx context.Context, dsn string, appName string, maxConns int32) (*pgxpool.Pool, error)` sets `statement_timeout=5s`.
  - `func pgtest.Cluster(t testing.TB) *TestCluster`. It uses `SAUTI_TEST_PG_DSN` if set; otherwise it starts embedded-postgres V18 once per package (in `TestMain`). Each call creates fresh databases with a random suffix, then migrates them.
  - `func (*TestCluster) DSN(store pg.Store, role string) string` and `func (*TestCluster) Admin(store pg.Store) *pgx.Conn` (connected as the owner).

- [ ] **Step 1: Write failing tests.** Every file below uses the `//go:build integration` tag.
  - `TestMigrateAppliesInOrderOnce`: a second run applies nothing.
  - `TestMigrateRefusesChangedChecksum`: uses an `fstest.MapFS` with an edited `0001`.
  - `TestMigrateSerializesConcurrentRunners`: two goroutines produce one application.
  - `TestBootstrapIsolatesStores`: `op_intake` cannot connect to the audit database; the error has SQLSTATE `42501`.
  - `TestBootstrapRefusesProduction`.
- [ ] **Step 2:** Run `go test -tags integration ./internal/platform/pg/...`. Expected: FAIL.
- [ ] **Step 3:** Implement. Each store gets a one-line placeholder migration `0000_init.sql` (`SELECT 1;`). It is replaced in Tasks 7–9, before any commit that includes those tasks.
- [ ] **Step 4:** Run the tests. Expected: PASS. Then commit: `feat(pg): checksummed forward-only migrator, dev bootstrap, test harness`.

### Task 7: Operational store: schema, single write path, roles, opstore adapter

**Files:**
- Create:
  - `db/operational/migrations/0001_core.sql`, `0002_write_path.sql`, `0003_grants.sql`
  - `db/operational/queries/incidents.sql`
  - `sqlc.yaml`
  - `scripts/tools/install-sqlc.sh`, `scripts/ci/sqlc-diff.sh`
  - `internal/platform/pg/opstore/{store.go,errors.go}` with generated code in `opstore/opdb/`
- Test: `internal/platform/pg/opstore/opstore_integration_test.go`

**Interfaces:**
- Consumes: the Task 4 `incident.Store` port and types, and the Task 6 harness.
- Produces:
  - `func opstore.New(pool *pgxpool.Pool) *Store`, which implements `incident.Store`.
  - `(*Store) Projection(ctx, id) (incident.Projection, error)`.
  - `(*Store) ListIncidentIDs(ctx) ([]uuid.UUID, error)`.
  - `(*Store) Claim(ctx, topic string, limit int) (audit.OutboxBatch, error)` implements `audit.Outbox`. The port is defined in Task 8, so Task 8 is built before this adapter's outbox code: write the outbox SQL here, and wire the Go adapter once Task 8's types exist.
  - SQLSTATE mapping: `SA404` → `ErrNotFound`, `SA409` → `ErrVersionConflict`, `SA422` → `ErrIllegalTransition` or `ErrSeverityLowered` (by message prefix), and `23505` on `incidents_reference_key` → `ErrReferenceTaken`.
- SQL facts this task fixes:
  - **Schema:** everything lives in `op`, with `search_path` set explicitly on every function: `SET search_path = op, pg_temp`.
  - **Tables:**
    - `op.incidents`: `reference` has `CHECK (reference ~ '^[0-9]{10}$')` and `UNIQUE`; `version bigint`; T-columns `t1`..`t7`; `severity`, `logic_version`, `escalated`, `flags text[]`, `merged_into`, `channel`.
    - `op.incident_events`: partitioned `BY RANGE (recorded_at)`; `PRIMARY KEY (id, recorded_at)`; columns as the Task 4 `Event`.
    - `op.event_keys(incident_id, idem_key, event_id)`, primary key `(incident_id, idem_key)`. This is the idempotency registry.
    - `op.sessions(id uuid PK, incident_id, channel, provider_session_id, phone_ct, phone_key_id, phone_mac, phone_mac_key_id, network_code, started_at)`, with `UNIQUE (channel, provider_session_id)`.
    - `op.ussd_steps(provider, provider_session_id, input_sha256 bytea, response text, end_session bool, incident_id, created_at)`, primary key on the first three columns.
    - `op.outbox(id uuid PK DEFAULT uuidv7(), topic, created_at, payload jsonb, delivered_at)`.
    - `op.incident_transitions`, seeded from the Task 4 table.
    - `op.config_versions`, `op.key_periods`.
  - **Partitions:** `op.ensure_event_partitions(months_ahead int)` creates monthly partitions plus `op.incident_events_default`. Migration 0001 calls it with 3. `op.default_partition_rows() RETURNS bigint`.
  - **`op.open_incident(...)`:** the parameters mirror `OpenParams` plus `p_idem_key`. It returns `(incident_id, reference, event_id, t1, created)`. On an existing `(channel, provider_session_id)`, including after catching `unique_violation` from a concurrent insert, it returns the existing incident with `created=false`.
  - **`op.append_incident_event(...)`:** follows the algorithm in spec §6.2. It locks the incident with `SELECT … FOR UPDATE` and raises `SA404`, `SA409` or `SA422` as appropriate. It writes the audit outbox payload, which has exactly the keys `entry_id` (equal to the outbox `id`), `event_id`, `occurred_at`, `event_type`, `incident_id`, `staff_id`, `logic_version` and `payload_sha256` (hex of `sha256(convert_to(payload::text,'UTF8'))`). `op.open_incident` writes the same payload for `session.started`.
  - **`op.claim_outbox(p_topic text, p_limit int)`:** returns `SETOF op.outbox`, locked `FOR UPDATE SKIP LOCKED`, ordered by `id`. `op.mark_outbox_delivered(p_ids uuid[]) RETURNS int`.
  - **Grants:**
    - `op_intake`: EXECUTE on `open_incident` and `append_incident_event`; SELECT on `incidents`, `sessions` and `incident_events`; SELECT and INSERT on `ussd_steps`.
    - `op_relay`: EXECUTE on `claim_outbox` and `mark_outbox_delivered` only.
    - `op_readonly`: SELECT on every `op` table.
    - No runtime role gets INSERT, UPDATE or DELETE on `incidents`, `incident_events`, `event_keys` or `outbox`.
  - **sqlc:** all queries are generated by sqlc v1.31.1. `scripts/tools/install-sqlc.sh` downloads `sqlc_1.31.1_linux_amd64.tar.gz` and verifies its sha256 `497ae4fcdfa64c5b0c311ffe4c2bd991e43991e82e5367792ed78bc2dca27354`. `sqlc.yaml` has `sql_package: pgx/v5` and `emit_pointers_for_null_types: true`.

- [ ] **Step 1: Write failing integration tests:**
  - `TestOpenIncidentSetsT1FromDatabase`: T1 lies within [before, after] of the database `clock_timestamp()` and its first event is `session.started`.
  - `TestAppendIsIdempotentOnKey`: the same idem key yields the same event ID and `Duplicate=true`, and the version is unchanged.
  - `TestAppendRejectsVersionConflict`, `TestAppendRejectsIllegalTransition`, `TestAppendRejectsSeverityLowering`.
  - `TestTransitionsTableMatchesGo`: the rows in `op.incident_transitions` equal `incident.Transitions()`.
  - `TestRuntimeRolesCannotWriteDirectly`: as `op_intake`, `UPDATE op.incident_events` and `INSERT INTO op.outbox` both fail with `42501`.
  - `TestRelayRoleCannotReadIncidents`.
  - `TestReferenceCollisionMapsToErrReferenceTaken`.
  - `TestProjectionMatchesFold`: the fixture from Task 4 gives a DB projection equal to `incident.Fold(Events)`.
  - `TestEventLandsInDefaultPartitionAndIsReported`.
  - `TestOutboxPayloadHasOnlyAllowedKeys`.
  - `TestSecurityDefinerFunctionsPinSearchPath`: `proconfig` contains `search_path=op, pg_temp`.
- [ ] **Step 2:** Run `go test -tags integration ./internal/platform/pg/opstore/...`. Expected: FAIL.
- [ ] **Step 3:** Write the migrations and queries, run `bin/sqlc generate`, then implement the adapter.
- [ ] **Step 4:** Run the tests, plus `scripts/ci/sqlc-diff.sh` (expect a clean diff). Expected: PASS. Then commit: `feat(opstore): operational schema, DB-enforced single write path, outbox`.

### Task 8: Audit store: schema, append function, adapter, canonical cross-check

**Files:**
- Create:
  - `db/audit/migrations/0001_ledger.sql`
  - `db/audit/queries/ledger.sql`
  - `internal/audit/{entry.go,canonical.go}`
  - `internal/platform/pg/auditstore/store.go` (generated code in `auditdb/`)
  - `docs/reference/audit-canonical-encoding.md`
- Test: `internal/audit/canonical_test.go`, `internal/platform/pg/auditstore/auditstore_integration_test.go`

**Interfaces:**
- Produces:
  - `type audit.Entry struct{ Seq int64; EntryID uuid.UUID; OccurredAt time.Time; EventType string; IncidentID, StaffID *uuid.UUID; LogicVersion string; PayloadSHA256, PrevHash, Hash [32]byte }`.
  - `type audit.AppendRequest struct{ EntryID uuid.UUID; OccurredAt time.Time; EventType string; IncidentID, StaffID *uuid.UUID; LogicVersion string; PayloadSHA256 [32]byte }`.
  - `func audit.Canonical(e Entry) []byte` and `func audit.ChainHash(prev [32]byte, e Entry) [32]byte`.
  - `type audit.Checkpoint struct{ Origin string; Seq int64; Head [32]byte; SignedAt time.Time; KeyID string; Signature []byte }`. Its `Body()` method and the signing logic come in Task 10.
  - The outbox port, consumed by Task 7's adapter and Task 10's relay:
    - `type audit.OutboxRow struct{ ID uuid.UUID; Payload json.RawMessage }`.
    - `type audit.OutboxBatch interface{ Rows() []OutboxRow; Complete(ctx context.Context, ids []uuid.UUID) error; Abort(ctx context.Context) }`.
    - `type audit.Outbox interface{ Claim(ctx context.Context, topic string, limit int) (OutboxBatch, error) }`.
  - `type audit.Ledger interface{ Append(ctx, AppendRequest) (Entry, error); Head(ctx) (int64, [32]byte, error); SaveCheckpoint(ctx, Checkpoint) error }`.
  - `type audit.LedgerReader interface{ Range(ctx, from int64, fn func(Entry) error) error; Checkpoints(ctx) ([]Checkpoint, error) }`.
  - `func auditstore.New(pool *pgxpool.Pool) *Store`, which implements both interfaces.
- Canonical encoding (normative; SQL and Go must match byte for byte):

```
canonical = "sauti-audit-entry/v1" ‖ 0x00
          ‖ lp(int8be(seq)) ‖ lp(uuid16(entry_id)) ‖ lp(int8be(occurred_at_unix_micros))
          ‖ lp(utf8(event_type)) ‖ lp(uuid16(incident_id) | empty) ‖ lp(uuid16(staff_id) | empty)
          ‖ lp(utf8(logic_version) | empty) ‖ lp(payload_sha256[32])
lp(x)     = int4be(len(x)) ‖ x
hash      = sha256(prev_hash ‖ canonical);   prev_hash of seq 1 = 32 zero bytes
```

- SQL facts:
  - **`audit.entries`:** typed columns only, with **no free-form column**. `staff_id` is `uuid`. `event_type CHECK (~ '^[a-z][a-z0-9_.]{2,63}$')`. `logic_version CHECK (<> '')`. Byte-length CHECKs of 32 on the hash columns.
  - **Write protection:** a `BEFORE UPDATE OR DELETE` row trigger and a `TRUNCATE` trigger raise errors.
  - **`audit.append(...) RETURNS audit.entries`:** runs `PERFORM pg_advisory_xact_lock(7347001)` as its own statement, returns the existing row for a known `entry_id`, then reads the head and inserts.
  - **`audit.head()`.**
  - **`audit.checkpoints(id bigserial, origin, seq, head bytea, signed_at, key_id, body bytea, signature bytea)`:** append-only through `audit.save_checkpoint(...)`.
  - **Grants:** `audit_appender` gets EXECUTE on `append`, `head` and `save_checkpoint` only; `audit_reader` gets SELECT.

- [ ] **Step 1: Write failing tests:**
  - `TestCanonicalGoldenVector`, against a committed hex fixture.
  - `TestChainHashGenesisUsesZeroPrev`.
  - `TestSQLHashMatchesGoCanonical`: 20 appends; Go recomputes every hash.
  - `TestAppendIdempotentOnEntryID`.
  - `TestConcurrentAppendsKeepValidChain`: 50 goroutines × 10 appends; `seq` runs 1..500 with no gaps and the chain verifies.
  - `TestAppenderCannotUpdateOrDelete`.
  - `TestOwnerUpdateBlockedByTrigger`.
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the tests. Expected: PASS. Then commit: `feat(audit): hash-chained ledger with canonical encoding and append-only enforcement`.

### Task 9: Analytic, linkage and media baseline schemas

**Files:**
- Create: `db/analytic/migrations/0001_meds.sql`, `db/linkage/migrations/0001_vault.sql`, `db/media/migrations/0001_media.sql`
- Test: `internal/platform/pg/schemas_integration_test.go`

**SQL facts:**
- **`analytic.meds_records`:** primary key `study_id uuid`. The MEDS fields (Guide §10):
  - `parish`, `landmark_code`
  - `time_of_call timestamptz`
  - `category`, `severity`
  - `responder_types text[]`
  - `interval_alert_to_arrival_s numeric(10,3)`
  - `first_action_code`
  - `facility_disposition`
  - `transport_mode`, `destination`
  - `status_24_48h`

  Further columns: `age_band`, `sex`; T-offsets `s_t2`..`s_t7 numeric(10,3)` (full resolution); `logic_version`, `zone_set_version`, `arrival_grade`, `evidence_flags text[]`; `consent_status CHECK (IN ('consented','guardian_permission','assent','waiver'))`; `waiver_reason`.
- **`analytic.suppression_ledger(study_id PK, withdrawn_at, reason CHECK IN ('withdrawal','dsr_deletion'))`:** append-only, enforced by a trigger.
- **`linkage.pairs(study_id PK, incident_id UNIQUE NOT NULL, created_at)`.**
- **`linkage.access_log(id, at, session_role, accessor, reason, study_id, incident_id, granted)`.**
- **`linkage.resolve_incident(p_study_id uuid, p_reason text, p_accessor text) RETURNS uuid`:** a `SECURITY DEFINER` function. Allowed reasons are `withdrawal`, `data_subject_request`, `payment_dispute` and `lawful_rec_uncst_request`. A disallowed reason logs `granted=false` and returns NULL; every call is logged.
- **`media.recordings(id, key_id, consent_ref, study_id, created_at, transcript_checked_at, destroy_by date NOT NULL)`** and **`media.chunks(recording_id, n, ciphertext)`.**
- **Grants:** as in spec §6.1. `lk_reader` gets EXECUTE on `resolve_incident` only.

- [ ] **Step 1: Write failing tests:**
  - `TestAllStoresMigrateFromZero`.
  - `TestAnalyticRejectsUnconsentedRecord`: `consent_status='pending'` violates the CHECK.
  - `TestLinkageResolveRequiresAllowedReason`: `reason='curiosity'` returns NULL and logs one row with `granted=false`.
  - `TestLinkageReaderCannotSelectPairs`.
  - `TestSuppressionLedgerAppendOnly`.
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the tests. Expected: PASS. Then commit: `feat(db): analytic, linkage vault and media baseline schemas`.

### Task 10: Relay, checkpointer, verifier, `cmd/outbox-relay`

**Files:**
- Create:
  - `internal/audit/{relay.go,checkpoint.go,verify.go,outbox.go}`
  - `internal/platform/health/health.go`
  - `internal/platform/metrics/metrics.go`
  - `cmd/outbox-relay/main.go`
- Test: `internal/audit/{relay_test.go,checkpoint_test.go,verify_test.go}`, `internal/platform/pg/relay_integration_test.go`

**Interfaces:**
- Consumes: Task 7 `(*opstore.Store).Claim`; Task 8 `Outbox`, `OutboxBatch`, `OutboxRow`, `Ledger`, `LedgerReader` and `Checkpoint`; Task 3 `crypto.Signer`, `crypto.Witness`, `crypto.VerifyMLDSA65`.
- Produces:
  - **Outbox:** `func ParseOutboxPayload(OutboxRow) (AppendRequest, error)` decodes strictly; an unknown key gives `ErrPoisonRow`.
  - **Relay:** `func NewRelay(o Outbox, l Ledger, log *slog.Logger, m *metrics.Registry) *Relay` and `(*Relay) RunOnce(ctx) (int, error)`. A poison row aborts the batch and returns `ErrPoisonRow`; it is never skipped.
  - **Checkpoints:**
    - `func (Checkpoint) Body() []byte` (the type is from Task 8), which gives `"sauti-audit-checkpoint/v1\n<origin>\n<seq>\n<base64std(head)>\n<RFC3339Nano UTC>\n"`.
    - `func NewCheckpointer(l Ledger, s crypto.Signer, w crypto.Witness, origin string, now func() time.Time) *Checkpointer` and `(*Checkpointer) Checkpoint(ctx) (Checkpoint, error)`. The witness file name is `checkpoint-<seq>-<unixnano>.txt`, containing the body plus `"— <keyid> <base64 sig>\n"`.
  - **Verifier:** `type VerifyReport struct{ Entries, HeadSeq, FirstBadSeq int64; Head [32]byte; CheckpointsOK int; Problem string }` and `func Verify(ctx context.Context, r LedgerReader, pub []byte) (VerifyReport, error)`. It checks sequence continuity, every hash, and that every checkpoint's head equals the chain hash at its `seq` with a valid signature.
  - **Health and metrics:**
    - `func health.Handler(live, ready map[string]health.Check) http.Handler` serves `/livez` and `/readyz` as JSON; any failing check returns 503.
    - `metrics.Registry` provides `Counter(name, help)`, `Gauge(name, help)` and `Handler()` (Prometheus text format, standard library only).
    - Metric names: `sauti_outbox_relayed_total`, `sauti_outbox_lag_seconds`, `sauti_outbox_poison_total`, `sauti_audit_checkpoints_total`.
  - **`cmd/outbox-relay`:**
    - Environment: `SAUTI_ENV`, `SAUTI_OP_DSN` (`op_relay`), `SAUTI_AUDIT_DSN` (`audit_appender`), `SAUTI_DEVKEYS_DIR`, `SAUTI_WITNESS_DIR`, `SAUTI_CONFIG_DIR`, `SAUTI_HTTP_ADDR`.
    - Leader election uses `pg_try_advisory_lock(7347002)` on a dedicated connection; a non-leader idles and stays live.
    - Readiness requires the operational DB only.

- [ ] **Step 1: Write failing tests.** Unit tests use in-memory fakes:
  - `TestRelayDeliversAllPending`.
  - `TestRelayRedeliveryIsIdempotent`: the fake ledger dedupes on `EntryID`.
  - `TestRelayAbortsOnPoisonRow`.
  - `TestRelayCatchesUpAfterLedgerOutage`: the ledger fails 3 times, the outbox is retained, then all rows are delivered in order.
  - `TestCheckpointSignedAndWitnessed`.
  - `TestVerifyDetectsBrokenHash`, `TestVerifyDetectsSeqGap`, `TestVerifyDetectsForgedCheckpoint`.

  Integration tests:
  - `TestRelayEndToEndAgainstPostgres`.
  - `TestSingleActiveRelayLeader`.
  - `TestAuditEntryNeverCarriesPayloadContent`: append an event whose payload contains `"0772123456"` through the operational write path, then relay it. No column of the resulting `audit.entries` row contains `772123456`.
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the tests. Expected: PASS. Then commit: `feat(audit): outbox relay, ML-DSA checkpoints, ledger verifier, outbox-relay process`.

### Task 11: Minimal intake, Africa's Talking USSD adapter, `cmd/intake-gw`

**Files:**
- Create:
  - `internal/intake/{service.go,steps.go}`
  - `internal/platform/pg/opstore/steps.go`
  - `internal/platform/africastalking/ussd.go`
  - `cmd/intake-gw/main.go`
- Test: `internal/intake/service_test.go`, `internal/platform/africastalking/ussd_test.go`, `internal/platform/pg/intake_integration_test.go`

**Interfaces:**
- Consumes: Task 4 `incident.Service.StartSession`; Task 5 `config.Screens`; Task 7 store.
- Produces:
  - `type intake.USSDRequest struct{ Provider, SessionID, ServiceCode, NetworkCode, Text string; Phone pii.Phone }`.
  - `type intake.Screen struct{ Text string; End bool }`.
  - `type intake.StepStore interface{ Lookup(ctx, provider, sessionID string, inputSHA [32]byte) (Screen, bool, error); Save(ctx, provider, sessionID string, inputSHA [32]byte, s Screen, incidentID uuid.UUID) (Screen, error) }`. `Save` returns the stored row: the first writer wins.
  - `type intake.Starter interface{ StartSession(ctx, incident.StartSession) (incident.Opened, error) }`.
  - `func intake.NewService(st Starter, steps StepStore, screens config.Screens, lang string) *Service` and `(*Service) HandleUSSD(ctx, USSDRequest) (Screen, error)`.
    - Empty `Text` → `StartSession`. The reply is `End=true` with `screens.StartedTestMode[lang]`, where `{ref}` is replaced by `Reference.Spoken()`.
    - Non-empty `Text` in M0 → the same incident's test-mode screen.
    - The idempotency key is `"ussd:"+provider+":"+sessionID+":"+hex(sha256(text))`.
  - `func africastalking.NewUSSDHandler(cfg USSDConfig, port USSDPort, log *slog.Logger) (http.Handler, error)`.
    - `USSDConfig{ Provider, PathToken string; ServiceCodes []string; MaxBody int64 }`.
    - The route is `POST /v1/ussd/at/{token}`, which answers 404 on a bad token (compared in constant time).
    - Unknown `serviceCode` → 403. Body limit 8 KiB.
    - The reply is `text/plain; charset=utf-8`, `"CON "` or `"END "` plus the text.
    - On an internal error the reply is `END` with `screens.InternalError`, which gives the toll-free line.
  - `cmd/intake-gw` reads `SAUTI_ENV`, `SAUTI_OP_DSN` (`op_intake`), `SAUTI_HTTP_ADDR`, `SAUTI_AT_PATH_TOKEN`, `SAUTI_USSD_SERVICE_CODES`, `SAUTI_DEVKEYS_DIR` and `SAUTI_CONFIG_DIR`. Readiness covers the operational DB only.

- [ ] **Step 1: Write failing tests:**
  - `TestATParsesFormAndRendersCONEND`.
  - `TestATBadTokenIs404`, `TestATUnknownServiceCodeIs403`, `TestATOversizeBodyRejected`.
  - `TestATInternalErrorGivesVoiceLine`.
  - `FuzzATForm`.
  - `TestHandleUSSDStartsSessionOnEmptyText`.
  - `TestRetriedCallbackReturnsSameScreenNoSecondEvent` (integration).
  - `TestConcurrentFirstCallbacksCreateOneIncident` (integration: 10 goroutines).
  - `TestScreensFitCharLimit`: every catalogue string, once rendered, is at most 160 characters.
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the tests. Expected: PASS. Then commit: `feat(intake): Africa's Talking USSD adapter, idempotent StartSession, intake-gw`.

### Task 12: `sautictl`, USSD simulator, M0 end-to-end demo

**Files:**
- Create: `cmd/sautictl/{main.go,migrate.go,bootstrap.go,keys.go,verify.go,checkpoint.go,demo.go}`, `tests/sim/ussd.go`, `tests/e2e/m0_fixture_test.go`
- Test: `tests/sim/ussd_test.go`, `tests/e2e/m0_fixture_test.go`

**Interfaces:**
- Produces:
  - `sautictl` subcommands:
    - `migrate --store <s> --dsn <owner dsn>`
    - `dev-bootstrap --admin-dsn <dsn> [--suffix] [--password-env]`
    - `dev-keygen --dir`
    - `verify-ledger --dsn <audit_reader> --pubkey-dir`
    - `verify-projections --dsn <op_readonly>`, which prints the first differing incident and exits 1
    - `checkpoint`
    - `demo m0`, which spins everything up in-process against a DSN and prints each step

    All exit non-zero on failure.
  - `func sim.NewUSSD(baseURL, token, serviceCode, network string) *USSD`.
  - `(*USSD) Dial(phone string) *USSDSession`.
  - `(*USSDSession) Send(input string) (Screen, error)`, where `Screen{Text string; End bool}` and an empty input means the first request.
  - `(*USSDSession) Retry() (Screen, error)` re-sends the last request byte for byte.

- [ ] **Step 1: Write the failing end-to-end test.** `TestM0FixtureIncident` (integration) covers spec §12.2 steps 1–7:
  1. Dial from the simulator. Assert `ParseReference` succeeds on the spoken reference in the screen, and `T1` is non-null.
  2. Append the fixture events through `incident.Service`.
  3. Run `RunOnce` until it returns 0.
  4. Check that the audit entry count equals the event count and that no entry contains the digits `772123456`.
  5. Take a checkpoint and confirm the witness file exists.
  6. Check that `Verify` reports `FirstBadSeq == 0` and `verify-projections` reports no drift.
  7. Tamper: as the owner, disable the trigger and flip one byte of entry 3's `hash`. `Verify.FirstBadSeq` must then equal 3.
  8. Retry: `Retry()` returns the identical screen, and the event count is unchanged.

  Also: `TestUSSDSimAccumulatesText` (`"1"`, then `"1*2"`).
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test -tags integration ./tests/...` and `go run ./cmd/sautictl demo m0`. Expected: PASS, with every step printed OK. Then commit: `feat: sautictl, USSD simulator, M0 end-to-end demo`.

### Task 13: Architecture check and CI pipeline

**Files:**
- Create:
  - `tools/archcheck/{main.go,check.go,rules.go}`
  - `scripts/ci/{fmt.sh,lint.sh,arch.sh,integration.sh,fuzz-smoke.sh,vuln.sh,sbom.sh,governance.sh,secrets.sh}`
  - `.github/workflows/ci.yml`
  - `.golangci.yml`
  - `build/tools.mod` (plus `.sum`)
  - `.github/CODEOWNERS`
  - `.coderabbit.yaml`
  - `.github/dependabot.yml`: none; Renovate arrives at P0, as `renovate.json` with `enabled:false` plus a note.
- Test: `tools/archcheck/check_test.go`

**Interfaces:**
- Produces:
  - `type Package struct{ ImportPath string; Imports, Deps []string }`.
  - `type Violation struct{ Package, Import, Rule string }`.
  - `func Check(module string, pkgs []Package) []Violation`.
  - `var Allowed = map[string][]string{ "pii": {}, "crypto": {"pii"}, "incident": {"pii","crypto"}, "audit": {"crypto"}, "config": {}, "intake": {"incident","pii","config"} }`.
- Rules:
  - R1: no `internal/<ctx>` package (where ctx ≠ platform) imports `internal/platform/...`.
  - R2: context-to-context imports must appear in `Allowed`.
  - R3: `net`, `net/http`, `os/exec`, `database/sql`, `github.com/jackc/pgx/...` and `github.com/valkey-io/...` may be imported only from `internal/platform/**`, `cmd/**`, `tests/**` or `tools/**`.
  - R4: only `cmd/pseudonymiser` may have both `internal/platform/pg/opstore` and `internal/platform/pg/analyticstore` in its `Deps`.
- CI jobs, in `ci.yml`:
  - **Triggers:** push and pull_request.
  - **Pinning:** every `uses:` is pinned by full SHA, resolved at implementation time with `gh api repos/<o>/<r>/git/ref/tags/<tag>`, with the tag kept in a trailing comment.
  - **Permissions:** `permissions: contents: read` by default.
  - **Jobs:** `fmt-lint`, `arch`, `unit` (`CGO_ENABLED=1 -race`), `integration` (a service `postgres:18.6` pinned by digest, with `SAUTI_TEST_PG_DSN`), `fuzz-smoke` (each fuzz target `-fuzztime=20s`), `sqlc-diff`, `vuln` (govulncheck and osv-scanner with the licence allowlist), `secrets` (gitleaks), `sbom` (cyclonedx-gomod, `-output-version 1.6`, uploaded as an artifact), `governance` (Task 14 scripts plus amendgate against `origin/main`).
  - **Tools:** `govulncheck` and `cyclonedx-gomod` run with `go tool -modfile=build/tools.mod`.

- [ ] **Step 1: Write failing tests:** `TestR1ContextMayNotImportPlatform`, `TestR2UnlistedContextEdgeRejected`, `TestR3NetOnlyInPlatform`, `TestR4OnlyPseudonymiserJoinsStores`, `TestRealModulePasses` (runs `go list -deps -json ./...` on this repository, expecting zero violations).
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement. Run `bash -n` on every script.
- [ ] **Step 4:** Run `go run ./tools/archcheck` (expect exit 0) and `scripts/ci/fmt.sh`. Then commit: `ci: architecture boundaries, hardened SHA-pinned pipeline, CODEOWNERS, CodeRabbit config`.

### Task 14: Governance gates and living documentation

**Files:**
- Create:
  - `docs/adr/README.md` and `docs/adr/ADR-0001..0017-*.md`. Topics in order:
    1. MADR adoption
    2. hexagonal monolith
    3. single write path
    4. outbox and relay
    5. audit ledger and checkpoints
    6. five stores and roles
    7. incident reference
    8. interim GitHub and confidential documents
    9. discrepancy resolutions D4–D6, O9 and O10
    10. migrator
    11. sqlc and PG18 dynamic DDL
    12. test database environments
    13. envelope encryption and dev keys
    14. config and amendment gate
    15. dependency policy and YAML v4
    16. living-docs mapping
    17. in-house metrics until the OpenTelemetry SDK
  - `docs/runbooks/_template.md` and these runbooks: `audit-ledger-verification-failure.md`, `outbox-relay-lag.md`, `outbox-poison-row.md`, `database-migrations.md`, `event-partition-maintenance.md`, `projection-drift.md`, `intake-gateway-errors.md`, `m0-demo.md`
  - `docs/tutorials/local-development.md`
  - `docs/reference/`: `configuration.md`, `environment.md`, `events.md`, `stores.md` (privilege matrix), `dependencies.md`, `cli-sautictl.md`, `ussd-at-adapter.md`
  - `docs/explanation/{architecture.md,security-model.md}`, with C4 context and container diagrams in Mermaid
  - `docs/traceability.md`, `docs/MODEL_CARD.md`, `.env.example`, `README.md`
  - `governance/{ai_risk_manifest.yaml,compliance_check.py,requirements.txt}`
  - `threat-model/{threats.yaml,validate_threats.py}`
  - `tools/__init__.py`, `tools/freshness/{__init__.py,__main__.py}`
- Test: the gate scripts run on fixtures: `governance/tests/test_compliance_check.py` and `tools/freshness/test_freshness.py` (`python3 -m unittest`)

**Rules the gates enforce:**
- **`compliance_check.py`:**
  - Every risk in `ai_risk_manifest.yaml` has `id`, `nist_function` (GOVERN, MAP, MEASURE or MANAGE), `iso42001_clause`, `owner_role`, `mitigation`, and a non-empty `verified_by` whose entries are `path::TestName` pairs, each existing as `func TestName` in that file.
  - `aibom_inventory` entries need `bom-ref`, `type` (`application` or `machine-learning-model`), `name`, `version`, `supplier`, `status` (`active` or `flagged-off`) and `data_residency: UG`.
  - Exits 1 on any violation.
- **`validate_threats.py`:** every threat has `id`, `stride` or `linddun` category, `asset`, `mitigations`, and `verified_by` test pointers checked the same way. Every hard constraint 1–14 is referenced by at least one threat or a traceability row.
- **`tools.freshness --check`:**
  - every backticked repo path in `docs/**/*.md` exists;
  - every ADR file is listed in `docs/adr/README.md` and each has the four MADR headings (`## Context and Problem Statement`, `## Decision Outcome`, `## Consequences`, `## Confirmation`);
  - every runbook has the template headings and `Last reviewed:`;
  - every `path::Test` pointer in `docs/traceability.md` resolves.
- **`requirements.txt`:** `PyYAML==6.0.3` with `--hash` lines.

- [ ] **Step 1: Write failing tests:** `test_missing_verified_by_fails`, `test_dangling_test_pointer_fails`, `test_valid_manifest_passes`, `test_adr_missing_heading_fails`, `test_unindexed_adr_fails`, `test_missing_path_fails`.
- [ ] **Step 2:** Run `.venv/bin/python -m unittest discover -s governance/tests` and the freshness tests. Expected: FAIL.
- [ ] **Step 3:** Implement the gates, then write all the docs.
- [ ] **Step 4:** Run `python3 -m tools.freshness --check`, `python3 governance/compliance_check.py`, `python3 threat-model/validate_threats.py` and `scripts/ci/governance.sh`. Expected: all exit 0. Then commit: `docs: ADRs, runbooks, Diátaxis reference, traceability, AI governance and threat model`.

### Task 15: Verification and pull request

- [ ] **Step 1:** Run every check locally. Each must exit 0 with the expected output:
  - `scripts/ci/fmt.sh`
  - `go vet ./...`
  - `go run ./tools/archcheck`
  - `scripts/ci/unit.sh`
  - `go test -tags integration ./...`
  - `scripts/ci/sqlc-diff.sh`
  - `scripts/ci/governance.sh`
  - `go run ./cmd/sautictl demo m0`
- [ ] **Step 2:** Push `m0-foundation` and open a PR against `main` using the `pr-ci-coderabbit-living-docs` skill. The body covers what M0 delivers, the demo output, what cannot be proven here, and links to the spec, the plan and the ADRs.
- [ ] **Step 3:** Poll `gh pr checks` until every check passes, fixing root causes as needed. Resolve every CodeRabbit comment.
- [ ] **Step 4:** Report to the user and **ask before merging**.
