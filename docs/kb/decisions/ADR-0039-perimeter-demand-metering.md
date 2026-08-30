# ADR-0039: Perimeter demand metering — hysteresis and honest gate accounting

- **Status:** Accepted
- **Date:** 2026-08-06
- **Amends:** ADR-0012 (scenario manifest gains optional `spawner` fields —
  hashed, so new run keys when used), ADR-0014 (metrics `demand` block
  gains additive suppression lines), ADR-0005 §5 (TSKF keyframe bumps to
  v8 for the gate bit — flag-pattern, v7-and-below bytes unchanged)
- **Scope:** kernel gating path (`engine/spawn.go`, `engine/director.go`),
  scenario manifest parsing/validation, metrics reporting. No NATS subject
  or payload changes; no record-plane change. Default OFF everywhere;
  `cap = 0` is bit-identical to today.

## Context

The Chicago-throughput mission (`docs/chicago-throughput-log.md`)
established on a valid harness (drain2/drain3, seed 42, 6 sim-hours): the
network drains, but 9–15% of trips strand in the signalized CBD core.
Sinks are wide open; the backlog is 100% workplace-bound; injection peaks
at 12.8k veh/h against a measured maximum discharge of ≈5.7k/h. The
drain2 macroscopic fundamental diagram is textbook: discharge crests at
≈5.7k/h around 4,800–5,100 active vehicles (≈2.2 veh/km) and bends down
beyond — the congested branch, with drain-side hysteresis (knots persist
after accumulation falls).

Real cities in this state meter and gate: perimeter control holds excess
demand outside the protected zone so the zone stays on the free-flow side
of its MFD crest. ADR-0036 (routing) and ADR-0038 (geometry) both help but
cannot repeal arithmetic: 12.8k in, 5.7k out.

### The gate already exists

`DensityTargetPerKm` (`spawner.density_per_km` in the manifest) caps
injection network-wide for both the deterministic spawner
(`engine/spawn.go`) and demand-director directives (`engine/director.go` —
"Density cap — exactly the Spawner's rule"). Held directives retry next
tick and — non-gate time only, since iteration 3b below — expire after
`DirectorSpawnHoldTicks` (600 ticks = 60 s) as `DirExpired`. It is
scenario-declared, ADR-0012-hashed, deterministic, and replay-free (replay
re-derives the gate from spec + recorded verbs; the director never re-runs).
What it lacks is hysteresis (a hard edge flickers at the boundary) and
honest accounting (a gate-held trip is indistinguishable from a
blocked-origin trip; nothing reports how much demand the gate suppressed).

## Decision

Milestone 1 of demand-side control, kernel-side, on the existing cap:

1. **Resume floor.** New optional manifest field
   `spawner.density_resume_per_km`. While density ≥ cap, injection holds;
   it resumes only when density falls below the floor. Unset → 0.9×cap.
   Floor ≥ cap is a manifest validation error. `cap = 0` (absent) disables
   the gate exactly as today — fixtures and all existing scenarios are
   bit-identical.
2. **Gate accounting.** Gate-held directives are counted distinctly:
   `DirGated` hold veh·ticks and a gate-caused share of `DirExpired`, per
   lane and in totals. Serve's end-of-run fidelity warning is gate-aware:
   gate suppression is reported as policy on its own line ("demand metering
   suppressed N trips … at the perimeter gate"), and the "of demand never
   entered the network" failure warning covers only the non-gate
   undelivered share.

   **Iteration 3b — the deferral clock (revises the expiry semantics
   above).** The original honesty decision had gate-held time count toward
   the 600-tick expiry window: a trip the gate never served was reported
   as not served. The drain4 pair (gate vs nogate, seed 42, 6 h) measured
   the consequence: stranded −20%, time loss −13%, VHT −23% — but completed
   7,656 vs 8,982 (−15%), falling ≈1:1 with the 1,731 gate-suppressed
   trips, because the gate DELETED trips after 60 s instead of deferring
   them. Real perimeter control queues demand for many minutes. So:
   gate-held ticks no longer count toward the `DirectorSpawnHoldTicks`
   origin-blockage window (that window still governs genuinely blocked
   origins, unchanged); the gate DEFERS. Deferral is bounded by the new
   manifest field `spawner.gate_hold_max_s` (default 1800 s; like the
   resume floor, a load error without a cap) — a directive gate-held past
   it expires as `DirGateExpired`, so the honesty stays in the accounting
   rather than in the semantics. `suppressed` veh·s spans the full gate
   wait by construction (`DirGated` counts every held directive-tick), and
   serve reports the resolved gate-wait distribution (count/mean/max).
   Caveat: the per-directive deferral clock (`dirGateHeld`) is engine
   state but NOT serialized in TSKF — a keyframe restore restarts it, so
   a seek across a long gate hold can shift that directive's expiry.
3. **Metrics.** The `demand` block gains additive, optional lines:
   `suppressed` (gate-held veh·s), `gated_expired` (trips expired while
   gate-held). `demandJSON` is already optional/additive; old recordings
   read unchanged.
4. **Example values, not engine defaults.** The chi scenario family gets
   manifest values anchored to the drain2 MFD: cap ≈2.0 veh/km
   (≈4,400 active), resume ≈1.8 veh/km (≈4,000). These pin the network just
   under the measured crest; the peak hour's ≈3,500 excess vehicles wait at
   origins as counted latent demand. Engine defaults stay 0/off — the gate
   is a scenario choice, and any default flip is its own measured decision
   (ADR-0036 precedent).

### Alternatives considered (deferred, not rejected)

- **Per-origin bounded-wait parameterization** — today's 600-tick
  `DirExpired` is the crude version; origins were not the bottleneck in
  drain2 (115 expiries vs 1,588 strands). Revisit if M1 shows origin-side
  unfairness.
- **Per-portal ALINEA ramp meters** — the real Chicago analog (Kennedy/Dan
  Ryan meters exist), spatially precise, but needs a per-portal tuning
  campaign. Revisit if M1's global gate misallocates (punishes
  far-from-core origins); the metrics accounting added here is what makes
  that visible.
- **Workplace-parking capacity** — finite destination capacity models the
  real CBD parking constraint (today's 400 workplace lanes are infinite
  sinks, which is why 33% of demand piles into the core unconditionally).
  A demand-model change with keyframed state: its own ADR, after the
  control levers are measured.
- **Core-district density as the gate signal** — a global density misreads
  spatial concentration (2.2 veh/km network-wide hides local core
  overload). Needs zone plumbing; M1 uses the standard first-order
  perimeter approximation and records this caveat.

## Determinism and replay

Gate state derives from engine state (`len(e.order)` / lane-km) at the
tick — no wall clock, no NATS timing, no external feedback channel.
Scenario fields ride the canonical manifest into the ADR-0012 content hash
(the `density_per_km` precedent); absent fields = 0 = today's behavior.
Replay re-derives holds/expiry from spec + recorded verbs exactly as it
does for the existing cap. No director CLI flags (the ADR-0036 RunMeta
lesson: behavior flags belong in the manifest so runs self-describe).

**Keyframe (TSKF v8).** Inside the [floor, cap) hysteresis band the gate
bit is not a function of current density, so it is keyframed: TSKF v8
carries it as one u64, written ONLY while the gate is declared (cap > 0)
AND engaged at marshal time — every other state marshals as v7 and below,
byte-identical to pre-v8 writers (the reader's re-derive, engaged iff
density ≥ cap, is exact outside the band). Restore of a v8 payload into a
cap-less spec is a loud error (the ADR-0036 flag-mismatch precedent);
restore of a v≤7 payload into a gate-on spec re-derives as before and
surfaces `RestoreNotice` when density sits inside the band. **How often v8
actually fires:** only on a keyframe taken while the gate is engaged —
i.e. warm starts of gate-on runs during an oversaturated episode; all
gate-off runs, all pre-engagement states, and every existing recording
stay on v7-and-below bytes. Pinned by `TestGateKeyframeMidBandRoundTrip`
(v8 written, bit restored, continuation CRC-exact),
`TestGateKeyframeCapZeroMarshalsBelowV8`, and
`TestGateKeyframeV8IntoCaplessSpecRejected`.

## Validation

1. CRC/M1–M3 fixtures bit-identical at cap=0 (pin with a fixture test; the
   existing `metricsjson_test.go` density-cap overload fixture already
   exercises the cap path).
2. Paired bracket on the consolidated chi network, gate on/off:
   seeds 1000–1003 (54k ticks, the standard bracket harness) + seed-42
   6-hour drain. Success = strands collapse toward the survivable regime,
   completions ≈ unchanged or better, peak active ≤ cap+ε, no new
   collision sections.
3. Accounting honesty: suppressed + gated_expired + completed + stranded +
   active + injected reconcile against the demand program total.
