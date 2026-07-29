# Chicago — the Loop and the expressways — upgrade options

Baseline `base`, 6 paired seeds, 12000 ticks, warmup 4000. Primary metric: `speed_kmh`.

> **These numbers are superseded and are being re-measured.** Two defects,
> both in the measurement rather than the scenario:
>
> 1. **Window.** This run emits lane intervals [4000,7000), [7000,10000) and
>    [10000,12000); the last is flagged `partial` because the horizon cut it
>    short. ADR-0014 §3 says comparison tooling drops partials — as of
>    2026-07-27 `whatif.py` and `corridorspeed.py` do, matching
>    `mkcongestionmap.py`, which always did. The table above was produced
>    before that fix, so it sums a truncated interval into a window it
>    reports as 12,000 ticks. The honest window for this run is 4,000–10,000,
>    which is what the quiz map's caption states.
> 2. **Fidelity.** These arms ran without `-drivers`, at roughly 1.5%
>    uncontrolled coasting — above the 0.1% bar `serve` warns at, and enough
>    that part of the fleet had no car-following control.
>
> The paired design means the RANKING is likely to survive both; the absolute
> speeds are not a measurement of the scenario as written. Re-run pending.

| option | Δ vs base | Δ% | p | Cohen's d | verdict |
|---|---:|---:|---:|---:|---|
| Shorter signal cycles — 34% shorter, same green share | 0.70 | 2.1% | 0.0021 | 2.38 | UPGRADE |
| Longer greens — 40% longer cycles citywide | -1.28 | -3.8% | 0.0000 | -6.46 | WORSE |
| Widen Lake Shore Drive — one lane each way | 0.17 | 0.5% | 0.0321 | 1.20 | no-op (under practical floor) |
| Widen the Kennedy — one lane each way | 0.06 | 0.2% | 0.3862 | 0.39 | no-op (n.s.) |

## Answer key

- **Shorter signal cycles — 34% shorter, same green share** (`retime-short`): UPGRADE, 2.1% on speed_kmh, p=0.0021
- **Longer greens — 40% longer cycles citywide** (`retime-long`): WORSE, -3.8% on speed_kmh, p=0.0000 — CAUTION: moves 2.7% less vehicle-distance (p=0.0001), so this is not a like-for-like comparison: the network is doing less work, not doing it better
- **Widen Lake Shore Drive — one lane each way** (`lsd-widen`): no-op (effect under the 1% practical floor; p-values below ~0.3% effect are not separable from run-to-run drift), 0.5% on speed_kmh, p=0.0321
- **Widen the Kennedy — one lane each way** (`kennedy-widen`): no-op (n.s.), 0.2% on speed_kmh, p=0.3862

## Everything tested

Raw verdicts — significance only, before the practical floor and the near-miss correction applied above. An option can read `no-op (n.s.)` here and `not demonstrated (near-miss)` in the shortlist; the shortlist wording is the one to use.

| option | Δ% | p | d | verdict |
|---|---:|---:|---:|---|
| retime-long | -3.8% | 0.0000 | -6.46 | WORSE |
| transit-5 | 2.1% | 0.0000 | 5.56 | UPGRADE |
| truck-ban | 2.3% | 0.0002 | 4.02 | UPGRADE |
| ramp-meter | 1.3% | 0.0009 | 2.86 | UPGRADE |
| retime-short | 2.1% | 0.0021 | 2.38 | UPGRADE |
| lsd-widen | 0.5% | 0.0321 | 1.20 | UPGRADE |
| kennedy-widen | 0.2% | 0.3862 | 0.39 | no-op (n.s.) |
