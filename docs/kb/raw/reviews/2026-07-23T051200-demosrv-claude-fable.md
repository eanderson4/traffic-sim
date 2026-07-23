Review complete. Verified against the working tree: `serve`'s `-scenario/-run/-ws/-seed/-ticks` flags (types match, seed/ticks are Uint64), serve's SIGTERM trap ("run abandoned"), `config.ts` default `ws://${hostname}:8443`, `scenario.Load`→`NetPath`, `engine.WriteGeoJSON`/`NetFile`, Go 1.25 (mux method patterns OK), and node 22.19 type-stripping for the `.ts` test imports — all consistent with the diff's claims.

## Findings

**No blockers.**

**Should-fix**

1. `engine/cmd/demosrv/supervisor.go:71-92,144-157` — `waitReady`/`waitPort` never watches `a.done`. A child that dies at startup (bad scenario file, stale port bind, engine panic) blocks `start` for the full 10 s and returns the generic "did not accept connections", burying the real failure — and the child's actual error is only in the log tee. Worse: if a stray process (e.g. a manually started `serve`) already listens on 8443, `waitPort` succeeds instantly even though the new child crashed, `start` returns OK, and the menu navigates to a map wired to the wrong engine. Select on `a.done` inside the poll loop and fail fast with "exited before ready". This is the launcher's core job (process babysitting), so the gap is in the primary path, but no committed config triggers it.

2. `engine/cmd/demosrv/registry.go:120-130` + `main.go:246` + `viz/src/demos-core.ts:33` — `validRunToken` admits URL metacharacters (`&`, `#`, `%`, `+`, `=`, `?`), yet `run` is interpolated raw into the query string on both sides; the "need no escaping" comment in `buildAppURL` is only true for `id`. Run `a+b` is a valid NATS token, but `URLSearchParams` decodes `+` as space → the viz subscribes to `ts.a b.state.snap` → silently blank map; `a&ws=x` injects a query param. Tighten `validRunToken` to the same `[A-Za-z0-9_-]+` charset as `id` (fail-loud at load, consistent with the ADR-0012 strict-fence precedent) — cheaper than escaping on both sides and keeps the byte-identical-URL invariant trivially true.

**Nit**

3. `engine/cmd/demosrv/demosrv_test.go:316,289` — in `TestHTTPEndpoints`, `cmds` is appended in the handler goroutine under `mu` but read (`cmds[0]`, `len`) on the test goroutine without it; there's no happens-before edge through the HTTP round-trip, so `go test -race` can flag this. Take `mu` for the reads.
4. `engine/cmd/demosrv/registry.go:74-96` — the validation errors format `"demos registry %s"` with `where` (e.g. `demos[0] (a)`), not the file path, so multi-registry setups can't tell which file failed. Decode errors include the path; validation errors don't.
5. `engine/cmd/demosrv/main.go` (routes) — the single supervisor mutex means `/api/status` and `/api/demo/stop` block up to `readyTimeout` (10 s) behind an in-flight start, so the menu's 3 s poll stalls and a hung start can't be cancelled. Bounded and localhost-only; fine to defer.
6. `engine/cmd/demosrv/main.go:230-260` — no Host/Origin check on the state-changing POSTs: any webpage can fire simple (no-preflight) cross-origin POSTs at `127.0.0.1:8900` to start/kill runs. Chrome's private-network access blocks this; other browsers don't. Localhost dev tool — flagging for the record, "too early" is a valid triage.

**Question**

7. `engine/cmd/demosrv/geojson.go` — the existence-based cache keys on demo `id`, so a registry edit repointing an id at a different `scenarioDir` (or a netconvert re-export) silently serves the stale network until the cache file is hand-deleted. The file header documents this as deliberate; confirming it's an accepted tradeoff rather than an oversight.

**Determinism / contract:** clean. No NATS subject or payload changes (localhost HTTP orchestration only, argued against ADR-0002 in the package doc and freshness note); run-token validation mirrors serve's flag contract; seed/ticks pass through explicitly so scenario determinism is untouched; `time.Now` is orchestration-plane only; no map iteration reaches any output (registry order is slice order). The `_placeholder` key in `demos.example.json` being rejected by `DisallowUnknownFields` is intentional and self-documented.

REVIEW-COMPLETE
