All cited anchors verified. Round-1 findings are genuinely addressed — B1 (vehicle_count self-delimiting), B3 (chunkBytes framing), B2 (`frame` descriptor matches proj.ts's `LocalFrame`), B4/S4 (sourceLayer, confirmed omitted at main.ts:1035/1039 today), seed/params in the content key, TSRL cadence math (45 MB raw / 0.5–1 MB/min checks out), half-open chunk ranges, lane home-tile ownership, edgeB baking (default-true fallback confirmed at main.ts:552), z13 gate (matches main.ts:594's internal minzoom), MaxUint32 guard, enumerated viz delta, deployment contract, overlays in the index. TSSF's 24 B record carries no speed field (frame.go:41, snapshots.ts:11), so TSRB's 14 B loses nothing — the synthetic re-encode is sound. MapLibre 5.24.0 confirmed in viz/package.json.

Remaining issues:

**Blockers:** none.

**Should-fix**

1. **Buffer formula self-contradicts and needs an unenumerated viz change.** ADR-0023 §2 (~line 183): `max(250, 1.25 × frameInterval/speed)` gives **625 ms** at 1× (1.25 × 500), but the text says "750 ms at 1×" — one of the two is wrong. Worse, `SnapshotBuffer.bufferMs` is `readonly`, set at construction (viz/src/snapshots.ts:48-57); "shrinking with playback speed" requires either a setter or a buffer rebuild on every speed change (which drops interpolation history mid-play). That mechanism is absent from item 6's enumerated delta — the exact class of omission round-1 S2 was about.

2. **`Content-Encoding: br` rule has no PMTiles carve-out.** §8 (~line 418) says pre-compressed objects are served "with their stored `Content-Encoding: br`" with no exception, while §7 (~line 389) has the pmtiles client doing HTTP Range reads against the same bucket. Range offsets into a `Content-Encoding`-compressed representation don't address the file's logical bytes (and CDNs may transcode); `network.pmtiles` must be stored/served identity — its tiles are internally compressed anyway. State the exclusion in the deployment contract.

3. **No clock below the zoom gate.** §6 item 4 (~line 359): below z13 "the shim schedules no frame fetches" — but the viz's current tick comes from `SnapshotBuffer.sample()` of received frames, so with no synthetic TSSF emitted, time freezes exactly in the zoomed-out state where the TSRL congestion applier ("look up the aggregate frame for the current tick", §6 item 1) is the whole show. The spec never says what advances the tick there (empty synthetic frames? the shim scheduler driving the applier directly?). Under-specified for the demo's primary zoomed-out view.

4. **The transport seam is `{ nc, close }`, not `{ close }`.** §6 (~line 306) claims the shim "returns the same `{ close }` shape" — but `SnapSubscription` exposes `nc` (nats-client.ts:13-16), and main.ts:976-977 does `natsConn = snapSub.nc; requestSig()` (the unconditional attach-time ADR-0016 signal pull; `requestSig` also fires from gap/partial paths at main.ts:373/387/393). The shim can't supply a `NatsConnection`; baked mode must leave `natsConn` null so `requestSig` no-ops (safe — the baked TSSG set is complete) — another delta item 6 should enumerate rather than paper over.

**Nits**

5. §2 angle quantization (~line 168): `round(angle mod 2π × 256/2π)` yields **256** for angles just under 2π — u8 overflow. Needs `mod 256` (or floor).
6. §4 calls the cadence field `strideFrames` (~line 246); the §5 example calls it `laneEveryFrames` (line 264). Pick one.
7. §7 (~line 378) says the casing default is a `coalesce`; it's actually `["case", ["boolean", ["get","edgeB"], true], …]` (main.ts:552). Same effect, wrong mechanism cited.
8. §1's CLI is `bake -store DIR -run RUN -out OUTDIR` (line 75), but §5's `overlays` (~line 296) says "bake copies them into the prefix when the demo has them" — overlays live in the demo deployment, not the recording store; the input (a flag? a dir convention?) is unspecified.
9. §5 `tickEnd: 9000` inclusivity is undefined; with 1801 frames = 15×120 + 1, the final chunk holds a single frame — fine, but worth one sentence so the shim's end-of-run/`done` logic isn't guessed at implementation.

**Questions:** none beyond #3.

REVIEW-COMPLETE
