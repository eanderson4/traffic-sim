#!/bin/sh
# demosrv container entrypoint: every public knob arrives via env so the
# Deployment manifest owns configuration (deploy/k8s/deployment.yaml).
# DEMOSRV_ADMIN_TOKEN gates the mutating endpoints (start/stop/replay
# control) via demosrv's env intake — stripped from engine children, never
# on argv (ADR-0020). Without it the pod refuses to boot — an
# unauthenticated public demosrv is worse than a crash-looping one.
set -eu
: "${DEMOSRV_ADMIN_TOKEN:?DEMOSRV_ADMIN_TOKEN must be set (secret traffic-sim-admin)}"
# serve's JetStream store (TMPDIR) needs its parent to exist; the emptyDir
# starts empty on every pod start, initContainer retry or not.
mkdir -p /data/tmp
exec /opt/bin/demosrv \
  -addr 0.0.0.0:8900 \
  -ws 0.0.0.0:8443 \
  -wspublic "${WS_PUBLIC_URL:-wss://ws.phantomjam.com}" \
  -autostart "${AUTOSTART_DEMO:-sf}" \
  -nobuild /opt/bin \
  -demos /srv/demos.json \
  -viz /srv/viz \
  -netcache /data/networks/.geojson-cache \
  -overlaydir /data/networks/overlays
