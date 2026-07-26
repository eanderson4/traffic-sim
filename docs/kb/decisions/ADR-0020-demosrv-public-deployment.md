# ADR-0020: demosrv public deployment (TLS-LB ws advertisement, admin token, autostart, prebuilt engines)

- **Status:** ACCEPTED
- **Date:** 2026-07-25

## Context

demosrv was built as localhost process orchestration in the spirit of
`pnpm dev` (its package doc): the operator sits next to it, clicks the
menu, and the open POST API is fine because the loopback audience is the
operator. The podcast demo program wants an always-on PUBLIC instance —
a GKE pod behind a GCE Ingress that terminates TLS and forwards 443
only. Four localhost assumptions break in that shape:

1. **The advertised engine ws URL is unreconstructable.** Clients are
   told `ws://<listen-addr>` (or the request host with the LISTEN port
   kept, `advertisedWsURL`). Behind the LB the browser must dial
   `wss://<public-name>` on 443 — no host/port rewrite of the pod's
   listen address can produce that.
2. **The mutating API is open.** `POST /api/demo/{id}/start`,
   `/api/demo/stop`, `/api/replay/{id}/start`, and the four replay
   control verbs kill/spawn the engine child or drive the replay. On the
   public internet that is an unauthenticated remote-kill.
3. **Nothing runs until a human clicks.** A freshly scheduled pod serves
   an idle menu; the demo must start itself.
4. **The pre-warm runs `go build`.** A minimal container image ships
   serve/replay prebuilt and may not have a Go toolchain (or the module
   tree `findEngineDir` looks for).

ADR-0004 (local-first) is not revoked: local docker-compose + processes
remains the primary dev shape. This ADR is additive — every flag
defaults to the pre-existing local behavior.

## Decision

demosrv gains four flags (`engine/cmd/demosrv`, deploy.go):

- **`-wspublic <url>`** — the client-facing engine WebSocket URL, used
  VERBATIM for every advertised ws URL (registry `ws`, `/api/demos`,
  demo/replay start deep links), bypassing `advertisedWsURL`'s host
  substitution. Startup-validated as an ABSOLUTE `ws://`/`wss://` URL —
  hostname required, no userinfo, no fragment (raw-`#` included), port
  numeric and in range (fatal otherwise: a typo would serve a
  healthy-looking menu whose map never connects). Setting `-wspublic`
  REQUIRES an admin token — it explicitly selects the public deployment
  shape, which must never serve the mutating POSTs open (fatal
  otherwise). Empty (default) = legacy derivation from `-ws`.
- **`-admintoken <tok>`** — when set, the MUTATING routes require
  `Authorization: Bearer <tok>` (constant-time compare; 401 with a fixed
  JSON error, no credential echo, `WWW-Authenticate: Bearer`). The gated
  set is exactly the POSTs: `POST /api/demo/{id}/start`,
  `POST /api/demo/stop`, `POST /api/replay/{id}/start`,
  `POST /api/replay/ctl/{pause,resume,speed,seek}`. All GETs stay
  public — menu, status, params, network/overlay fetches are the
  audience's. Intake: a SUPPLIED flag wins; an ABSENT (default) flag
  falls back to the `DEMOSRV_ADMIN_TOKEN` environment variable (K8s
  Secret → env, because argv is world-readable via `/proc/*/cmdline`
  and `kubectl describe`). Unset everywhere (default) = open, local dev
  unchanged. Intake is whitespace-trimmed (the echo-into-Secret newline
  footgun) and presence-aware: ANY explicitly supplied token that trims
  to empty — flag (incl. `-admintoken=`) or env (incl. set-but-empty) —
  fails startup CLOSED rather than deploying the gate open. The token is
  stripped from all child environments (engine children AND the go-build
  pre-warm) — it is demosrv's own credential, and the children expose
  the unauthenticated ws plane described below.
- **`-autostart <demo-id>`** — once the HTTP listener is BOUND, start
  the demo directly through the supervisor (not via the HTTP route: the
  internal start bypasses the token gate, which the pod itself holds no
  token for). Up to 4 attempts with doubling backoff; exhaustion or an
  unknown id logs and KEEPS SERVING — a pod with no demo running is
  debuggable, a crash-looping one is not. Terminal conditions beyond
  exhaustion: an operator's start winning the race (left alone), any
  operator STOP (never un-stopped), and shutdown. ONE-SHOT: after a
  successful start the loop exits for good — a demo that crashes later
  leaves the pod idle (crash recovery is the manifests' job, see
  Consequences). Only within the retry window does a run that exits on
  its own get a fresh attempt. Note for the manifests: an HTTP liveness
  probe on demosrv does NOT detect a dead demo — demosrv keeps answering
  GETs with 200; the probe must inspect `/api/status` (idle while
  `-autostart` was configured) or the engine ws port (Fable, round 25).
  And even that signal is ambiguous: unknown-id, exhausted retries, an
  intentional operator stop, and a post-success crash ALL read as idle —
  restart-on-idle would un-do operator stops and crash-loop permanent
  startup failures, so the recovery policy (what idle means, whether to
  restart) is a manifests design decision, not something demosrv can
  decide (sol, round 27). One residual edge, accepted: within the retry
  window an operator-started run that CRASHES is not distinguished from
  idle, so a retry may replace it (a start, unlike a stop, leaves no
  terminal marker — Fable, round 27).
- **`-nobuild <dir>`** — skip the go-build pre-warm; exec `<dir>/serve`
  and `<dir>/replay`. Both are stat-checked at startup (exist +
  executable) with a fatal, explicit error — a demosrv that discovers a
  missing engine on the first start click has already answered the
  menu's GETs and looks healthy.

## Consequences

- demosrv now has a security boundary, but ONLY when `-admintoken` is
  set — the default local behavior is otherwise unchanged (one
  unconditional delta, benign locally: a 10 s `ReadHeaderTimeout` on
  demosrv's HTTP server, added for the public shape).
- **DEPLOYMENT PRECONDITION (sol review, 2026-07-25): the engine's
  WebSocket plane itself is UNAUTHENTICATED** — the embedded broker
  (ADR-0006 §8 single-binary demo, `DontListen` + ws `NoTLS`, no
  users/permissions) accepts publishes as well as subscriptions from any
  client that reaches it, including `ts.{run}.ctl.intent.>` and the
  JetStream APIs. This is pre-existing, not introduced here, and these
  flags do not change it — but `-wspublic` exists to put that port on
  the public internet, where ADR-0006 §9's observer-read-only discipline
  is currently enforced by NOTHING. Before the public ws endpoint is
  exposed, the broker needs an auth/permissions design (observer account
  with subscribe-only, engine/controller accounts with publish) — an
  ADR-0006 amendment with its own design round, plus viz credential
  plumbing. Treat this as gating the go-live, not this commit.
- **SECOND GO-LIVE PRECONDITION (deploy review, 2026-07-25): `/healthz`
  on the engine ws listener** — the LB BackendConfig health-checks it on
  the ws port and the engine serves no HTTP paths today. The two
  preconditions are INDEPENDENT and may land in either order (healthz is
  trivial; broker auth is the design round); both must land before
  `deploy/k8s/ingress.yaml` is applied. See deploy/README.md.
  Constraint on the auth amendment: `/healthz` must stay UNAUTHENTICATED —
  the GCE health checker carries no credentials, so an auth scheme that
  covers it would keep the ws backend permanently unhealthy.
- **DEFERRED (deploy review): child-aware liveness.** demosrv's
  `/api/status` is 200 with a dead engine child; k8s probes therefore
  can't see a crashed run. v1 accepts up-to-6-hour idle windows
  (CronJob restart cadence); the fix is a demosrv health endpoint that
  reflects child state with intentional-stop semantics — an engine change
  with its own round, tracked in deploy/README.md.
- The token gate sits at the mux, so the internal autostart path is
  ungated by construction rather than by an exemption list; autostart
  spawns only while demosrv is up (a shutdown channel aborts its retry
  loop, a supervisor latch refuses starts after the final stop) and
  never replaces a run an operator started (atomic idle guard in the
  supervisor). One deliberate semantic change rides with this: a start
  racing a queued STOP is now REFUSED (409, `start aborted`) instead of
  queueing — the stop would otherwise kill the freshly spawned child
  after a full readiness wait.
- `-wspublic` makes the operator authoritative over what browsers dial;
  demosrv stops trying to be clever about it (the loopback/wildcard
  heuristic remains for the local cases it was designed for).
- The ws port still binds inside the pod on `-ws`; the LB (or a
  sidecar) owns TLS. demosrv remains plain HTTP/ws internally — no TLS
  stack enters this codebase.
- Deploy manifests (image build, Ingress, token plumbing) live in
  `deploy/` (this ADR covers only the demosrv-side contract). `-autostart`
  is ONE-SHOT: a demo that crashes after a successful start leaves the
  pod idle — crash recovery is the manifests' job (K8s liveness/pod
  restart), not a demosrv supervisor loop.
- KNOWN GAP (deferred): the viz menu and replay panel send no
  `Authorization` header, so with `-admintoken` set their start/stop/ctl
  buttons 401 — the public UI is effectively view-only, with starts done
  by `-autostart` or an operator's token-bearing client. A token-aware
  menu UX is a follow-up in the viz workstream (sol review, 2026-07-25),
  deliberately not bundled into this demosrv-side change.
- CONSCIOUS DEFERRAL: with GETs public, `/net/{file}` serves city-scale
  chunked GeoJSON (hundreds of MB) unauthenticated, and the replay
  ctl/status proxy (`withActiveReplay`, pre-existing) holds the
  supervisor mutex across its downstream call — flooding it starves
  start/stop. Egress/abuse/rate-limiting is an infra concern (LB/CDN),
  deliberately not demosrv's (Fable + sol, 2026-07-25).
