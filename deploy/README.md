# deploy

**Planned, P0** (`docs/superpowers/plans/2026-11-12-phased-roadmap.md`, phase 9; spec §5, §11). Nothing is built here yet.

Infrastructure as code for the hosts in Uganda, in four planned homes:

- `ansible/`: the Ansible roles for every host and process;
- `quadlet/`: the Podman Quadlet units, one per process;
- `haproxy/`: HAProxy and keepalived configuration;
- `patroni/`: the Patroni and etcd configuration for PostgreSQL 18.

Constraints: data stays in Uganda (hard constraint 1); images are pinned by digest (spec §9); PostgreSQL stays at 18 until Patroni supports 19 (spec §13). Owner: the repository owner (`*` in `.github/CODEOWNERS`).

Where this sits in the reference architecture: [`docs/reference/repository-layout.md`](../docs/reference/repository-layout.md).
