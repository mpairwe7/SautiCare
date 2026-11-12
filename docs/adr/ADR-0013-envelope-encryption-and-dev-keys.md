---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: ISP §5.3; RMRP §4
informed: Principal Investigator, Data Protection Officer
---
# ADR-0013: Envelope encryption, row-bound AAD, keyed lookup hash and development keys

## Context and Problem Statement

Identifiers must be encrypted at rest with keys held apart from data (ISP §5.3), destroyable per period (crypto-shredding, RMRP §4), and a redial must find its incident within the resume window without decrypting anything.

## Decision Drivers

* AES-256 at rest; keys separate from data
* Ciphertext must not be movable between rows
* Retention by key destruction

## Considered Options

* AES-256-GCM with AAD binding store/table/column/row; HKDF-derived HMAC-SHA-256 lookup hash per key period; KeyProvider port
* Database-native encryption (pgcrypto)
* Deterministic encryption for lookups

## Decision Outcome

Chosen option: **application-level AES-256-GCM** with `AAD = sauti-aad/v1 ‖ length-prefixed(store, table, column, row id)`; the phone lookup hash is HMAC-SHA-256 under a key derived from the period's data key, checked for the current and previous period. Key material arrives through `KeyProvider`/`Signer` ports: OpenBao 2.7 with an HSM in production (P0); `internal/platform/devkeys` (0600 file) in development, which refuses `SAUTI_ENV=production`, as do the processes and `sautictl dev-keygen`.

## Consequences

* Good: destroying a period key makes every copy unreadable, including backups.
* Bad: until P0, processes cannot run in production by design.

## Confirmation

`internal/crypto/crypto_test.go::TestOpenFailsWithSwappedAAD`, `::TestLookupMACsMatchesPreviousPeriod`; `internal/platform/devkeys/devkeys_test.go::TestDevkeysRefuseProduction`, `::TestDevkeysFileIsPrivate`.
