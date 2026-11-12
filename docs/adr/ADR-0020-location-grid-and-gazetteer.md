---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: Build Prompt §9, §12
informed: Field Coordinator, PI
---
# ADR-0020: UTM 36N grid in Go and a frozen gazetteer with pg_trgm matching

## Context and Problem Statement

Location comes from Sauti's own landmark lists mapped to 500 m zones, without Google Maps or a GIS server (Build Prompt §12). Callers choose a ward and a landmark from menus, or type up to 20 characters. Dispatch needs a zone; the dedup rule needs zone distance. How are positions computed, stored and matched?

## Decision Drivers

* Exact, testable geodesy with no external service
* Gazetteer versions frozen during the window; a correction is a new version plus a deviation record
* Free text never leaves the caller's ward and never reaches an event payload

## Considered Options

* Transverse Mercator (Krüger series) in Go; gazetteer as gated YAML loaded into versioned, immutable tables; `pg_trgm` similarity within the ward
* PostGIS
* Matching in Go (Levenshtein) over an in-memory list

## Decision Outcome

Chosen option: **Go grid + versioned gazetteer tables + `pg_trgm`**.
- `internal/location` converts WGS84 to EPSG:32636 with the Krüger series to sixth order (verified against PROJ to 1 mm) and takes cells as `floor(E/500), floor(N/500)`. EPSG:32636 has no false northing, so rows south of the equator are negative and are accepted everywhere.
- `config/gated/gazetteer.yaml` is parsed and validated (code IDs, at most five menu landmarks per ward, reserved `elsewhere` and `other`). `sautictl gazetteer load` stores a version once, immutably: triggers refuse UPDATE, DELETE and TRUNCATE even for the owner. The version in force is recorded in `op.config_versions`.
- Free text is normalised and matched by `op.match_landmark` within the ward. At or above `fuzzy_match_min_similarity` it gives the landmark's cell (quality `fuzzy`). Below it gives the ward centre plus the `needs_location_callback` flag, and dispatch proceeds on the ward's zones.
- `pg_trgm` is installed in its own schema `trgm` with no USAGE for runtime roles: a trusted extension's functions stay executable by PUBLIC and the store owner cannot revoke that.
- The gazetteer is outside the dispatch-parameter fingerprint. It has its own version, carried on `incident.dispatch_ready`.

## Consequences

* Good: no GIS server; deterministic cells; a gazetteer cannot be edited in place.
* Bad: until the field team collects the per-ward lists, a synthetic placeholder gazetteer is used and production refuses it.

## Confirmation

`internal/location/grid_test.go::TestToUTM36NMatchesReferenceVectors`; `internal/location/gazetteer_test.go::TestParseFileRejectsInvalidGazetteers`; `internal/platform/pg/opstore/gazetteer_integration_test.go::TestGazetteerIsFrozen`, `::TestMatchIsScopedToWard`, `::TestRuntimeRolesCannotReachTrigramFunctions`; `internal/intake/service_test.go::TestLowConfidenceFreeTextUsesWardAndFlags`.
