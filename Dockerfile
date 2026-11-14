# Sauti Dispatch demo, one image for a Hugging Face Space (Docker SDK).
# Synthetic data only, erased on every restart (ADR-0039). How to build, run
# and deploy it: docs/runbooks/demo-hf-space.md.
#
# Images are pinned: Go to CI's exact toolchain, PostgreSQL to the digest of
# postgres:18.6 that CI's integration job runs (.github/workflows/ci.yml).

FROM golang:1.27.4 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Every command, statically linked. The SQL migrations are compiled into the
# binaries (go:embed in db/migrations.go), so the image needs no db/ tree.
RUN mkdir -p /out && go build -ldflags="-s -w" -o /out/ ./cmd/...

FROM postgres:18.6@sha256:e3d24f6906d0760bf4da0dae872415391ededefad23fd471c47dc53b0ddf3c42

# The runner starts PostgreSQL 18 from the base image's binaries. Their
# directory is a Debian packaging detail: fail the build here, not at
# start-up on the Space, if it moves or is not version 18.
ARG PG_BIN=/usr/lib/postgresql/18/bin
RUN test -x "$PG_BIN/initdb" && test -x "$PG_BIN/postgres" \
 && "$PG_BIN/postgres" --version | grep -q ' 18\.'

# Hugging Face runs the container as uid 1000; the base image's postgres
# user is uid 999. Create uid 1000 unless the base image already has it, and
# give the runner a data directory it can write to. 1777, like /tmp: each run
# lives in its own 0700 sauti-demo-* directory, so the demo still starts if
# the platform picks another unprivileged uid. PostgreSQL refuses to run as
# root, and so does the runner.
RUN set -eux; \
    getent group 1000 >/dev/null || groupadd --gid 1000 demo; \
    getent passwd 1000 >/dev/null || useradd --uid 1000 --gid 1000 --create-home --home-dir /home/demo --shell /usr/sbin/nologin demo; \
    install -d -o 1000 -g 1000 -m 1777 /app/var

WORKDIR /app
COPY --from=build /out/ /app/bin/
# What the services read at run time: configuration (with the gazetteer) and
# the signed triage bundle.
COPY config/ /app/config/
COPY triage-rules/ /app/triage-rules/
RUN set -eux; \
    for b in demo intake-gw dispatch-worker outbox-relay; do test -x "/app/bin/$b"; done; \
    test -f /app/config/gated/gazetteer.yaml; \
    test -f /app/triage-rules/tool1.yaml.sig

USER 1000:1000
ENV HOME=/tmp
EXPOSE 7860
# The base image stops PostgreSQL with SIGINT; the runner wants SIGTERM, then
# stops its children and PostgreSQL itself.
STOPSIGNAL SIGTERM
# The base image's entrypoint would start a PostgreSQL server of its own.
ENTRYPOINT ["/app/bin/demo", "--pg-bin", "/usr/lib/postgresql/18/bin", "--data", "/app/var", "--addr", ":7860", \
            "--bin-dir", "/app/bin", "--config-dir", "/app/config", "--rules-root", "/app"]
CMD []
