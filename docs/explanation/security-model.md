# Security and privacy model

**Type:** Explanation (Diátaxis) · **Threat model:** `threat-model/threats.yaml` · **Verification standard:** OWASP ASVS 5.0 (Level 2; Level 3 for authentication and the linkage vault)

Sauti processes health data about people in emergencies, including children, so its posture follows the sensitivity of the data rather than the size of the company (Engineering Guide §15). The controls below map to the Information Security Policy (ISP v3.0) by section number. The policy text is confidential and is not reproduced here.

| Concern | Control in M0 | ASVS 5.0 chapter | Policy |
|---|---|---|---|
| Data stays in Uganda | egress allowlist; no foreign services; (P0) nftables default-deny | V13, V14 | ISP §4, §5.4, §9 |
| Separation of identifiable and research data | five databases, per-store roles, CONNECT revoked from PUBLIC; archcheck R4 | V8, V15 | ISP §5.1–5.2 |
| Linkage vault | reason-gated resolution, every attempt logged | V8, V16 | ISP §5.1, §5.5 |
| Encryption at rest | AES-256-GCM with row-bound AAD; per-period keys; dev keys refuse production | V11, V14 | ISP §5.3; RMRP §4 |
| Tamper evidence | SHA-256 chain, append-only triggers, ML-DSA-65 checkpoints, witness | V16 | ISP §5.5 |
| No PII in telemetry | masking types; structural log redaction; audit stores hashes only | V16 | ISP §5.6 |
| Authoritative time | database-set timestamps | V16 | ISP §5.2 |
| Input handling at the edge | form parsing limits, constant-time token, service-code allowlist, fuzzing | V1, V2, V4 | — |
| Supply chain | SHA-pinned actions, digest-pinned images, checksum-verified tools, SBOM, govulncheck, OSV, gitleaks | V15 | Build Prompt §17 |
| Change control | amendment gate; the merge gate `tools/mergegate/__init__.py` and ruleset 27260742 ([ADR-0042](../adr/ADR-0042-fail-closed-merge-gate.md)); CODEOWNERS (not enforced, by the owner's decision O-1 in the evaluation audit) | V15 | Build Prompt rule 3.11 |

**Post-quantum position.** Checkpoints use ML-DSA-65 now, because the audit and analytic records are kept for five years and NIST IR 8547 retires quantum-vulnerable signatures after 2030. TLS with the X25519MLKEM768 hybrid key exchange arrives with the P0 infrastructure.

**Known gaps (tracked).**
- Production key management (OpenBao + HSM), mTLS and passkey login arrive in P0 and M5.
- Both processes refuse `SAUTI_ENV=production` until then.
- The independent penetration test happens before go-live (M7).
