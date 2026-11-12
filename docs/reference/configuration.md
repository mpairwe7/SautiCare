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
| `config/gated/dispatch.yaml` | `mode` | `protocol` | ring 1 only until the decisions 1, 2, 6 amendment |
| | `ring_step_metres` / `ring_interval` | `500` / `60s` | Build Prompt §10.4, decision 2 |
| | `max_radius_metres` | `2500` | decision 2 |
| | `cascade_timeout` | `90s` | Build Prompt §10.4; Guide §6 |
| | `red_parallel_count` | `3` (per role) | Protocol (O9) |
| | `forward_ack_timeout` | `60s` | decision 2 |
| | `voice_fallback_after` | `20s` | decision 8 |
| | `ranking_order` | `zone_distance, response_history, least_recently_dispatched` | decision 4 **open**: safe default |
| | `ranking_version` | `default-2026-11-12` | stamped on incidents (M2) |
| `config/gated/flags.yaml` | `android_app`, `caller_link`, `speech_assist`, `app_api` | all `false` | decision 10 |
| `config/gated/screens.yaml` | `started_test_mode`, `internal_error` (`en`, `lg`) | test-mode copy; Luganda `PENDING-TRANSLATION` | blocks production |
| `config/platform.yaml` (ungated) | `checkpoint_interval` | `5m` | Build Prompt §4 |
| | `outbox_batch_size` / `relay_poll_interval` | `100` / `1s` | tuning |

`Bundle.ProductionReady()` refuses to start with `SAUTI_ENV=production` while `protocol_status` is not `approved`, or while any screen contains `XXX` or `PENDING-TRANSLATION`.

**What is gated.** The gated paths are `config/gated/**`, `config/study.yaml`, `triage-rules/**` and the gate list `governance/gated-paths.txt` itself. The effective gate is the union of this built-in minimum, the list at the base ref and the list in the change, so a change can only widen it. Gated paths must be regular files, not symlinks, and are hashed as tracked at HEAD. `config/platform.yaml` is ungated operational tuning by design.

**Changing a value.** Before the study window, change it in a pull request. During the window, add an amendment record to `governance/amendments.yaml` holding the ethics reference, the approval date and the sha256 of the new file content; CI rejects the change otherwise.
