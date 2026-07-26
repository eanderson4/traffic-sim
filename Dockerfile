# syntax=docker/dockerfile:1
# Public demos image for phantomjam.com — demosrv + prebuilt engine children
# (serve/replay, run with demosrv -nobuild) + built viz + the pruned public
# demos registry. Scenario data and the geojson cache are NOT in the image;
# an initContainer pulls them from gs://roaring-bots-traffic-sim into a
# shared emptyDir mounted at /data (deploy/k8s/deployment.yaml).

FROM golang:1.25-bookworm AS go
WORKDIR /src
COPY engine/go.mod engine/go.sum engine/
RUN cd engine && go mod download
COPY engine/ engine/
RUN cd engine && CGO_ENABLED=0 go build -trimpath -o /out/ ./cmd/demosrv ./cmd/serve ./cmd/replay

FROM node:22-bookworm-slim AS viz
RUN corepack enable && corepack prepare pnpm@10.20.0 --activate
WORKDIR /src/viz
COPY viz/package.json viz/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY viz/ ./
RUN pnpm build

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=go /out/demosrv /out/serve /out/replay /opt/bin/
COPY --from=viz /src/viz/dist /srv/viz
COPY deploy/demos.public.json /srv/demos.json
COPY deploy/docker-entrypoint.sh /opt/bin/entrypoint
# CWD is / so the registry's repo-root-relative paths (data/scenarios/…)
# resolve onto the initContainer-populated emptyDir at /data.
WORKDIR /
ENTRYPOINT ["/opt/bin/entrypoint"]
