# Configuration reference

**Type:** Reference (Diátaxis) · **Source of truth:** `config/` · **ADR:** [ADR-0014](../adr/ADR-0014-configuration-and-amendment-gate.md)

Behavioural configuration is versioned YAML in Git, loaded by `internal/config/load.go` with strict decoding: an unknown key is an error. Everything under `config/gated/` counts toward the **dispatch-parameter fingerprint** (`sha256:<hex>`), which is stamped as the logic/parameter version. While `study_window_active` is true, the amendment gate (`tools/amendgate`) freezes all of it. Deployment settings such as DSNs and addresses come from environment variables instead ([environment.md](environment.md)).

| File | Key | Value (2026-11-12) | Source / decision |
|---|---|---|---|
| `config/study.yaml` | `study_window_active` | `false` | Build Prompt rule 3.11 |
| `config/gated/protocol.yaml` | `protocol_status` | `pending` | Protocol v2.0 and Tools 1–7 not yet received; blocks production |
| | `protocol_version` | `"2.0"` | |
| `config/gated/intake.yaml` | `resume_window` | `10m` | D4, ADR-0009 (Guide §4) |
| | `dispatch_trigger_screen` | `7` | D5, ADR-0009 (Build Prompt §10.1) — defines T2 |
| | `default_screen_char_limit` | `160` | O7: until MTN/Airtel confirm in writing |
| | `screen_char_limits` | `{}` | per network code once confirmed |
| | `fuzzy_match_min_similarity` | `0.45` | Build Prompt §12: below it, ward-level location + `needs_location_callback`; field-tested in M7 |
| | `abandon_after` | `120s` | Build Prompt §10.3, Guide §4: abandonment flag and danger-yes escalation |
| | `dedup_window` / `dedup_max_zone_distance` | `15m` / `1` | Build Prompt §10.3: same category, within one zone, 15 minutes (ADR-0022) |
| `config/gated/tool1.yaml` | the eight screens' copy, options, danger questions, first-aid lines, messages | **placeholder** (`status: placeholder`, `PENDING-TOOL1`) | Tool 1 not received; [tool1-flow.md](tool1-flow.md); blocks production |
| `config/gated/triage.yaml` | `rules_file`, `trusted_keys` | `triage-rules/tool1.yaml`; one `dev-` key | ADR-0019: who may sign clinical rules; a `dev-` key blocks production |
| `config/gated/gazetteer.yaml` | wards, landmarks, aliases, stages, facilities (versioned) | synthetic `placeholder-2026-11-13` | ADR-0020; separately versioned (not in the fingerprint); `sautictl gazetteer load` |
| `triage-rules/tool1.yaml` (+ `.sig`, `scenarios.yaml`) | signed decision table | `tool1-placeholder-0.1`, `protocol_status: pending` | ADR-0019; gated |
| `config/gated/dispatch.yaml` | `mode` | `protocol` | ring 1 only until the decisions 1, 2, 6 amendment |
| | `ring_step_metres` / `ring_interval` | `500` / `60s` | Build Prompt §10.4, decision 2 |
| | `max_radius_metres` | `2500` | decision 2 |
| | `cascade_timeout` | `90s` | Build Prompt §10.4; Guide §6 |
| | `red_parallel_count` | `3` (per role) | Protocol (O9) |
| | `forward_ack_timeout` | `60s` | decision 2 |
| | `voice_fallback_after` | `20s` | decision 8 |
| | `red_escalation_after` | `3m` | decision 2 (protocol answer): coordinator alerted for red with a role unaccepted, both modes |
| | `stopping_rule` | `{window: 30, max_failure_rate: 0.15}` | Guide §14, O6: red dispatch failure above 15% across 30 consecutive red incidents suspends dispatch automatically |
| | `ranking_order` | `zone_distance, response_history, least_recently_dispatched` | decision 4 **open**: safe default |
| | `ranking_version` | `default-2026-11-12` | stamped on incidents (M2) |
| `config/gated/flags.yaml` | `android_app`, `caller_link`, `speech_assist`, `app_api` | all `false` | decision 10 |
| `config/gated/screens.yaml` | `internal_error` (`en`, `lg`) | names the free voice line; Luganda `PENDING-TRANSLATION` | blocks production |
| `config/platform.yaml` (ungated) | `checkpoint_interval` | `5m` | Build Prompt §4 |
| | `outbox_batch_size` / `relay_poll_interval` | `100` / `1s` | tuning |

`Bundle.ProductionReady()` refuses to start with `SAUTI_ENV=production` while `protocol_status` or the Tool 1 catalogue's `status` is not `approved`, or while any screen contains `XXX`, `PENDING-TOOL1` or `PENDING-TRANSLATION`. `wiring.BuildIntake` also refuses a placeholder gazetteer and a pending or `dev-`signed triage bundle. `config.Load` fails on any file under `config/gated/` it does not use.

**What is gated.** The gated paths are `config/gated/**`, `config/study.yaml`, `triage-rules/**` and the gate list `governance/gated-paths.txt` itself. The effective gate is the union of this built-in minimum, the list at the base ref and the list in the change, so a change can only widen it. Gated paths must be regular files, not symlinks, and are hashed as tracked at HEAD. `config/platform.yaml` is ungated operational tuning by design.

**Changing a value.** Before the study window, change it in a pull request. During the window, add an amendment record to `governance/amendments.yaml` holding the ethics reference, the approval date and the sha256 of the new file content; CI rejects the change otherwise.
