# api/proto/sauti/v1

**Planned, M5.** Nothing is built here yet.

The Protobuf contracts of the coordinator console API: the services, messages and server streams that `cmd/console-api` serves and `web/console` calls, generated with buf v2 for Connect RPC v2 (spec §5, §14).

- **Milestone:** M5 Console (`docs/superpowers/plans/2026-11-12-phased-roadmap.md`, phase 6: "Connect v2 console API with streams").
- **Owner:** the repository owner (`*` in `.github/CODEOWNERS`).
- **Constraints:** a message never carries the caller's number (hard constraint 5); personal data reaches a coordinator only through the audited reveal (roadmap phase 6).

Where this sits in the reference architecture: [`docs/reference/repository-layout.md`](../../../../docs/reference/repository-layout.md).
