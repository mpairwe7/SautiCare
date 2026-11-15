# web/console

**Planned, M5.** Nothing is built here yet.

The coordinator console: a React 19 single-page app built with Vite 8 (versions in spec §14). It shows the live board on a MapLibre map with PMTiles, tasks, logs, the study view and responder history, and talks only to `cmd/console-api` through the contracts in `api/proto/sauti/v1`.

- **Milestone:** M5 Console (`docs/superpowers/plans/2026-11-12-phased-roadmap.md`, phase 6; spec §5), proven by a Playwright and axe suite.
- **Owner:** the repository owner (`*` in `.github/CODEOWNERS`).
- **Constraints:** no access from outside Uganda (hard constraint 13); personal data appears only through the API's audited reveal (roadmap phase 6). Build output and test reports are gitignored.

Where this sits in the reference architecture: [`docs/reference/repository-layout.md`](../../docs/reference/repository-layout.md).
