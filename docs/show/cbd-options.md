# Chicago Loop CBD — upgrade options

Baseline `base`, 12 paired seeds, 12000 ticks, warmup 4000. Primary metric: `speed_kmh`.

| option | Δ vs base | Δ% | p | Cohen's d | verdict |
|---|---:|---:|---:|---:|---|
| Shorter cycles AND a peak-hour freight ban together | 0.53 | 6.7% | 0.0028 | 1.10 | UPGRADE |
| Slow Streets — 25 km/h limit across the Loop grid | -0.69 | -8.8% | 0.0000 | -2.06 | WORSE |
| Free CTA transfers — shift 5% of Loop trips to transit | 0.32 | 4.0% | 0.0704 | 0.58 | not demonstrated (near-miss) |
| Peak-hour freight ban — no trucks in the Loop | -0.01 | -0.1% | 0.9743 | -0.01 | no-op (n.s.) |

## Answer key

- **Shorter cycles AND a peak-hour freight ban together** (`retime-truckban`): UPGRADE, 6.7% on speed_kmh, p=0.0028
- **Slow Streets — 25 km/h limit across the Loop grid** (`calm-secondary`): WORSE, -8.8% on speed_kmh, p=0.0000 — CAUTION: moves 4.8% less vehicle-distance (p=0.0008), so this is not a like-for-like comparison: the network is doing less work, not doing it better
- **Free CTA transfers — shift 5% of Loop trips to transit** (`transit-5`): not demonstrated — the estimate clears the 1% floor but the test does not reach p<0.05 at this sample size. Say "we could not show it works", NOT "it does nothing": this is a power limit, not a null result, 4.0% on speed_kmh, p=0.0704
- **Peak-hour freight ban — no trucks in the Loop** (`truck-ban`): no-op (n.s.), -0.1% on speed_kmh, p=0.9743

## Everything tested

Raw verdicts — significance only, before the practical floor and the near-miss correction applied above. An option can read `no-op (n.s.)` here and `not demonstrated (near-miss)` in the shortlist; the shortlist wording is the one to use.

| option | Δ% | p | d | verdict |
|---|---:|---:|---:|---|
| calm-secondary | -8.8% | 0.0000 | -2.06 | WORSE |
| retime-truckban | 6.7% | 0.0028 | 1.10 | UPGRADE |
| retime-short | 3.6% | 0.0578 | 0.61 | no-op (n.s.) |
| transit-5 | 4.0% | 0.0704 | 0.58 | no-op (n.s.) |
| truck-ban | -0.1% | 0.9743 | -0.01 | no-op (n.s.) |
