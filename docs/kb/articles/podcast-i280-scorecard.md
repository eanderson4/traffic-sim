# Podcast Scorecard — I-280 Woodside upgrade variants (2026-07-24)

> The bit: show the guest a congested corridor, propose four upgrades, they pick
> the best. This file is the read-aloud scorecard: what each option did in the
> sim, with the receipts. Corridor: I-280 / Woodside (the advocacy reference
> network). Baseline pain: corridor entry demand raised from today's
> 600 veh/h/lane to **1500 veh/h/lane** (2.5× — the degradation knee found in
> the 2026-07-24 demand sweep: mean speed 24.7 → 18.6 m/s within 15 minutes).
> All runs seed 42, one simulated hour (36000 ticks @ dt 0.1), recorded at
> pace 8 (cross-pace metric comparison is invalid — all four used the SAME
> pace, so the table is internally valid).

## The four options

- **A — Do nothing** (`i280-pod-base`): 1500 veh/h/lane for the hour.
- **B — Ramp metering** (`i280-pod-meter`): hold entry demand at
  1200 veh/h/lane, the rate the corridor absorbed cleanly in the sweep.
- **C — Truck diversion** (`i280-pod-trucks`): same 1500 total, but the 10%
  truck share removed (diverted off-corridor).
- **D — Signal retiming** (`i280-pod-retime`): both ramp-terminal programs
  shortened from a 90 s to a 60 s cycle, same green share.

## Finals — full simulated hour, seed 42 (ADR-0014 §6 metric intervals)

| Option | Mean speed | Time lost | Delay per km | Stops | Throughput (VMT) |
|---|---|---|---|---|---|
| A do nothing   | 6.2 m/s (22 km/h)  | 316 veh-h | 125.5 s/km | 5060 | 9,066 km |
| B metering     | **21.1 m/s (76 km/h)** | **35 veh-h** | **11.7 s/km** | **53** | **10,789 km** |
| C truck divert | 6.0 m/s (22 km/h)  | 324 veh-h | 129.8 s/km | 4905 | 8,975 km |
| D retiming     | 6.8 m/s (25 km/h)  | 291 veh-h | 110.5 s/km | 4643 | 9,485 km |

## What the sim says

- **Metering wins on every axis — including throughput.** It is not "fewer
  cars": the metered run DELIVERS 19% more vehicle-kilometres than doing
  nothing, at 3.4× the mean speed, with 9% of the delay and 1% of the stops.
  Holding the on-ramp at capacity keeps the mainline out of the jam regime,
  where the corridor actually carries more.
- **Truck diversion does nothing reliable.** At the 15-minute probe scale it
  looked WORSE than doing nothing on seed 42 (15.1 vs 18.6 m/s) and identical
  on seed 43 (12.5 both); over the full hour it lands on top of the baseline
  (6.0 vs 6.2 m/s). Once the corridor is past the knee, the 10% truck share
  is not the binding constraint.
- **Retiming is a provable no-op — with receipts.** The corridor's two
  signalized junctions bind only ramp-terminal internal lanes, and the demand
  never crosses them: all six signal-bound lanes recorded ZERO distance in
  the metric intervals. The small A-vs-D differences are not physics — the
  run key is (content-hash, seed), so the retimed network's hash draws a
  different Poisson arrival stream. Same congestion, different dice.
- **Congestion compounds.** At 15 minutes the baseline reads 18.6 m/s —
  merely bad. By the end of the hour the same demand has ground the corridor
  to 6.2 m/s: the queue keeps accumulating because arrival rate never drops
  below discharge rate. The full-hour recordings are the dramatic visuals;
  the first 15 minutes are the "can you spot it early?" cut.

## Seed robustness (15-minute probes, both seeds)

| Option | seed 42 | seed 43 |
|---|---|---|
| A base-1500   | 18.6 m/s, 52k s loss | 12.5 m/s, 99k s loss |
| B meter-1200  | 24.7 m/s, 13k s loss | 25.0 m/s, 10k s loss |
| C trucks      | 15.1 m/s, 79k s loss | 12.5 m/s, 98k s loss |

At 1500 the corridor is past its capacity knee and regime-noisy — the two
seeds disagree by 6 m/s on the SAME scenario. Metering is the only option
that is robust across seeds. Showing both seeds on air is the trust signal:
the sim is honest about stochastic regimes, not a single scripted movie.

## How to show it

- Demo page → REPLAY cards `rec-pod-base` / `rec-pod-meter` /
  `rec-pod-trucks` / `rec-pod-retime`. VCR controls: pause, seek, 1–8×.
- The congestion overlay (per-lane speed ratio) reads red on the metered
  run only at the on-ramp; everywhere on the others.
- ⚠ Operational: the full-hour recordings are ~3.3 GB each; the replay child
  materializes a recording in memory at startup (~40 s, ~23 GB RSS for 36000
  ticks) which EXCEEDS demosrv's 10 s child-readiness timeout — use the
  15-minute demo recordings (`*-15m`) for live clicking, the full-hour ones
  for the metrics above. Logged as a work-queue item (readiness timeout +
  replay materialization footprint).

## Provenance

Variants: `data/scenarios/i280-pod-{base,meter,trucks,retime}/variant.yaml`
(ADR-0012 overlays on `i280-woodside`). Recordings:
`data/recordings/i280-pod-*` (+ `.metrics.json`). Demand sweep:
`data/scenarios/i280-sweep-{1200,1500,1800}`. All `data/` paths are local
(gitignored). Metrics schema: ADR-0014 §6 per-lane interval aggregates
(Σdist/Σtime = mean speed; time_loss vs free-flow).
