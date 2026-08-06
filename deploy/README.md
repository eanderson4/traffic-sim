# Deploying the public demos (app.phantomjam.com)

Target: GKE cluster `carbon-dev` (project `roaring-bots`, us-central1),
behind the existing GCE Ingress. One pod, one replica, forever — demosrv
supervises a single active run; horizontal scaling is meaningless here.

Routing: `/` serves the splash landing page (`viz/index.html` →
`dist/index.html`); the demos menu stays at `/demos.html` and the map app
at `/app/` (all from the built viz in `/srv/viz`).

Caveat (2026-08-05): the splash's featured CTA is baked-first
(`viz/src/splash.ts` BAKED_FEATURED_URL) and targets the Cloudflare Pages
bundle, where `/baked/*` is staged by `scripts/show/mksite.sh`. demosrv
serves no `/baked/*` route, so on THIS deploy the featured card's link
404s until either the bake plane is served here too or splash.ts grows a
probe-and-fall-back. The live-run deep links (`/app/…`) are unaffected.

## GO-LIVE PRECONDITIONS — do not `kubectl apply` the Ingress until these land

1. **Broker auth on the engine ws plane** (ADR-0020 §go-live): the embedded
   NATS ws listener is unauthenticated and publish-capable; exposing
   `ws.phantomjam.com` without the auth/permissions amendment lets any
   internet client inject intents into the shared run.
2. **`/healthz` on the engine ws listener**: `backendconfig.yaml`
   health-checks it on 8443; the engine serves no HTTP paths today, so the
   ws backend would be permanently unhealthy and every ws dial would 502.

Both are engine changes with their own review cycle. Until then these
manifests are staged infrastructure, not a deployable system.

## One-time setup (done 2026-07-25)

- Node pool: `traffic-pool`, 1× e2-standard-2, label `workload=traffic`
  (LA-lean's engine does not fit the 4 GB e2-medium default nodes).
- Data bucket: `gs://roaring-bots-traffic-sim` — `scenarios/<city>-lean/`
  (network JSON + scenario.yaml) and `netcache/` (pre-baked v2 geojson,
  keyed by SHA-256 of the exact network bytes — geojson.go — so cache
  files only match the network they were generated from). The
  deployment's initContainer re-pulls on every pod start, so updating
  data = upload + `kubectl rollout restart`.
- Bucket IAM: default compute node SA has `roles/storage.objectViewer`.
- DNS (Cloudflare, manual): A records `app.phantomjam.com` and
  `ws.phantomjam.com` → the reserved static IP **136.68.88.9**
  (`gcloud compute addresses describe traffic-sim-ip --global`),
  DNS-only (no proxy — the ManagedCertificate needs to see the LB).
  This ingress gets its OWN load balancer; do not point the domain at
  the pre-existing shared ingress IP (34.8.180.100).

## Deploy / update

```sh
# 1. image (from repo root; requires the demosrv flags -wspublic/-autostart/
#    -nobuild and the engine /healthz endpoint).
#    UNIQUE tag per build — per-HEAD alone ignores dirty trees and rebuilds,
#    so add seconds + entropy; re-pushing a referenced tag produces no
#    rollout and can serve a stale cached image.
TAG=v0.1.0-$(git rev-parse --short HEAD)-$(date +%Y%m%d%H%M%S)-$(head -c2 /dev/urandom | od -An -tx1 | tr -d ' ')
docker build --platform linux/amd64 -t us-central1-docker.pkg.dev/roaring-bots/carbon-dev-repo/traffic-sim:$TAG .
docker push us-central1-docker.pkg.dev/roaring-bots/carbon-dev-repo/traffic-sim:$TAG

# 2. admin token (once; generate a real one). NOTE: re-running this rotates
#    the token (kubectl apply overwrites), invalidating the old one — and the
#    RUNNING pod keeps the OLD token until restarted (env is injected at pod
#    creation), so follow any rotation with:
#    kubectl rollout restart deployment/demosrv -n traffic-sim
kubectl create namespace traffic-sim 2>/dev/null || true
kubectl -n traffic-sim create secret generic traffic-sim-admin \
  --from-literal=token="$(openssl rand -hex 24)" --dry-run=client -o yaml | kubectl apply -f -

# 3. workloads — sed substitutes the unique tag into the :TAG placeholder;
#    applying the whole directory would reset the image, so always deploy
#    through this pipe, never plain `kubectl apply -f deploy/k8s/`.
#    ingress.yaml is EXCLUDED until the go-live preconditions above land.
for f in namespace service backendconfig deployment cronjob; do
  echo "---"
  sed "s|:TAG|:$TAG|" deploy/k8s/$f.yaml
done | kubectl apply -f -

# 4. go-live (ONLY after both preconditions land; no :TAG in this file):
kubectl apply -f deploy/k8s/ingress.yaml

# 5. inspect the token for operator use (start/stop/switch runs)
kubectl -n traffic-sim get secret traffic-sim-admin -o jsonpath='{.data.token}' | base64 -d
```

## Operating

- Public viewers are read-only: they see the autostarted demo (env
  `AUTOSTART_DEMO`, default `sf`) and can watch whatever is running.
- Switching runs is a token-gated POST:
  `curl -X POST -H "Authorization: Bearer $TOK" https://app.phantomjam.com/api/demo/<demo-id>/start`
  (route per engine/cmd/demosrv main.go; check `kubectl logs` on failure).
- LA-lean driver attach takes ~7-15 min on 2 vCPU — but demosrv pins the
  child's `-attach-timeout 600s` (10 min, measured on a dev box). An LA
  switch on this hardware can time out at the top of that range; verify
  once via port-forward before advertising LA (remedy: bump the node pool
  or a demosrv flag for the timeout — an engine change).
- LA-lean memory under the 6Gi limit is UNVERIFIED (world build + driver
  attach for 1.38M lanes). Verify with a port-forwarded switch before
  advertising LA; if it OOMKills, raise the limit and/or the node pool.
- Crash recovery: `deploy/k8s/cronjob.yaml` restarts the pod every 6 h
  (fresh run via autostart, well inside the 10-sim-hour tick horizon).
  Between CronJob ticks a crashed engine child leaves the site idle —
  the probes hit demosrv's `/api/status`, which stays 200. Operator
  fallback: `kubectl rollout restart deployment/demosrv -n traffic-sim`.
  Demos run with `ticks: 360000` = 10 sim-hours.
- Startup is all-or-nothing on the data bucket: demosrv stat-checks every
  scenarioDir at boot, so one missing `<city>-lean` prefix in
  `gs://roaring-bots-traffic-sim/scenarios/` crash-loops the whole pod.
  Verify bucket contents after any data update (all six cities + non-empty
  netcache, both verified present 2026-07-25).
- The ManagedCertificate provisions only after DNS points at the LB;
  `kubectl get managedcertificate -n traffic-sim` should reach Active.
- The bucket has no `overlays/` prefix, so `/overlay/*` 404s by design
  (the flag tolerates a missing dir). If a demo's viz ever expects zone
  overlays, add the prefix to the bucket + one initContainer line.
