# analytics

**Planned, M6.** Nothing is built here yet.

Work done inside the analytic enclave: notebooks, metric SQL and the disclosure-control scripts that check outputs before release (spec §5). The primary analysis is descriptive; PyMC is for labelled exploratory work only (spec §2, D6).

- **Milestone:** M6 Research data (`docs/superpowers/plans/2026-11-12-phased-roadmap.md`, phase 7: "metric SQL and fixtures", "disclosure control"; spec §11, M6).
- **Owner:** the repository owner (`*` in `.github/CODEOWNERS`). The analytic store's schema, `db/analytic/`, is meant for the Principal Investigator.
- **Constraints:** it reads pseudonymised analytic data only, never the operational or linkage stores (hard constraint 9); outputs pass small-cell checks (requirement R15); data stays in Uganda (hard constraint 1).

Where this sits in the reference architecture: [`docs/reference/repository-layout.md`](../docs/reference/repository-layout.md).
