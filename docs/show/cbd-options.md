# Chicago Loop CBD — upgrade options

Baseline `base`, 18 paired seeds, 12000 ticks, warmup 4000. Primary metric: `speed_kmh`.

| option | Δ vs base | Δ% | p | Cohen's d | verdict |
|---|---:|---:|---:|---:|---|
| Free CTA transfers — shift 5% of Loop trips to transit | 0.27 | 3.4% | 0.0122 | 0.66 | UPGRADE |
| Slow Streets — 25 km/h limit across the Loop grid | -0.79 | -9.9% | 0.0000 | -1.69 | WORSE |
| Peak-hour freight ban — no trucks in the Loop | 0.15 | 1.9% | 0.1958 | 0.32 | no-op (n.s.) |
| Longer greens — 40% longer cycles on the arterials | -0.12 | -1.6% | 0.2834 | -0.26 | no-op (n.s.) |

## Answer key

- **Free CTA transfers — shift 5% of Loop trips to transit** (`transit-5`): UPGRADE, 3.4% on speed_kmh, p=0.0122
- **Slow Streets — 25 km/h limit across the Loop grid** (`calm-secondary`): WORSE, -9.9% on speed_kmh, p=0.0000 — CAUTION: moves 6.3% less vehicle-distance (p=0.0001), so this is not a like-for-like comparison: the network is doing less work, not doing it better
- **Peak-hour freight ban — no trucks in the Loop** (`truck-ban`): no-op (n.s.), 1.9% on speed_kmh, p=0.1958
- **Longer greens — 40% longer cycles on the arterials** (`retime-long`): no-op (n.s.), -1.6% on speed_kmh, p=0.2834

## Everything tested

| option | Δ% | p | d | verdict |
|---|---:|---:|---:|---|
| cordon-20 | 11.7% | 0.0000 | 2.16 | UPGRADE |
| calm-secondary | -9.9% | 0.0000 | -1.69 | WORSE |
| transit-5 | 3.4% | 0.0122 | 0.66 | UPGRADE |
| truck-ban | 1.9% | 0.1958 | 0.32 | no-op (n.s.) |
| retime-long | -1.6% | 0.2834 | -0.26 | no-op (n.s.) |
| retime-short | 1.7% | 0.3192 | 0.24 | no-op (n.s.) |
| widen-secondary | 2.1% | 0.3459 | 0.23 | no-op (n.s.) |
