---
status: accepted
date: 2026-11-12
decision-makers: Sauti engineering (repository owner)
consulted: Repository owner (decision D2)
informed: Principal Investigator, Data Protection Officer
---
# ADR-0008: Interim GitHub hosting, confidential documents excluded, Forgejo-ready CI

## Context and Problem Statement

The Build Prompt places the monorepo and CI on Forgejo in Uganda; during development the repository is a public GitHub repository and the global rules gate merges on GitHub CI and CodeRabbit. The governing documents (ISP v3.0 is "Confidential, internal") contain staff contact details.

## Decision Drivers

* Personal data never leaves Uganda (rule 3.1) — code and synthetic data may
* Confidential policies must not be published
* One CI definition for both forges

## Considered Options

* GitHub now, Forgejo-ready scripts; confidential documents gitignored
* Private GitHub repository
* Forgejo only

## Decision Outcome

Chosen option: **GitHub now, Forgejo-ready**. CI logic lives in `scripts/ci/*.sh`; workflows are thin wrappers pinned by commit SHA, so Forgejo Actions runs the same scripts at P0. The governing PDFs, `sa/`, any `protocol/` folder and `.docx` files are gitignored; documentation cites them by title and section only. Real inventories, addresses and keys never enter the repository.

## Consequences

* Good: CodeRabbit and GitHub CI available now; no lock-in.
* Bad: architecture and triage rules are public; security relies on no secrets in Git (gitleaks scans full history).

## Confirmation

`scripts/ci/secrets.sh` (gitleaks over full history) in CI; `.gitignore` excludes the governing documents; this ADR is revisited at P0 when the repository moves to Forgejo.
