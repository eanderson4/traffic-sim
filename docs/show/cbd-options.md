# Chicago Loop CBD — upgrade options

Baseline `base`, 10 paired seeds, 12000 ticks, warmup 4000. Primary metric: `speed_kmh`.

| option | Δ vs base | Δ% | p | Cohen's d | verdict |
|---|---:|---:|---:|---:|---|
| transit-5 | 0.40 | 5.0% | 0.0419 | 0.75 | UPGRADE |
| calm-secondary | -0.75 | -9.5% | 0.0027 | -1.30 | WORSE |
| retime-long | -0.26 | -3.4% | 0.1269 | -0.53 | no-op (n.s.) |
| truck-ban | 0.08 | 1.0% | 0.3634 | 0.30 | no-op (n.s.) |

## Answer key

- **transit-5** (`transit-5`): UPGRADE, 5.0% on speed_kmh, p=0.0419
- **calm-secondary** (`calm-secondary`): WORSE, -9.5% on speed_kmh, p=0.0027 — CAUTION: moves 6% less vehicle-distance than baseline, so this is not a like-for-like comparison: the network is doing less work, not doing it better
- **retime-long** (`retime-long`): no-op (n.s.), -3.4% on speed_kmh, p=0.1269
- **truck-ban** (`truck-ban`): no-op (n.s.), 1.0% on speed_kmh, p=0.3634

## Everything tested

| option | Δ% | p | d | verdict |
|---|---:|---:|---:|---|
| cordon-20 | 9.3% | 0.0007 | 1.58 | UPGRADE |
| calm-secondary | -9.5% | 0.0027 | -1.30 | WORSE |
| transit-5 | 5.0% | 0.0419 | 0.75 | UPGRADE |
| retime-long | -3.4% | 0.1269 | -0.53 | no-op (n.s.) |
| truck-ban | 1.0% | 0.3634 | 0.30 | no-op (n.s.) |
| widen-secondary | 1.4% | 0.4590 | 0.24 | no-op (n.s.) |
| retime-short | -0.8% | 0.7492 | -0.10 | no-op (n.s.) |
