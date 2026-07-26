# Chicago — the Loop and the expressways — upgrade options

Baseline `base`, 6 paired seeds, 12000 ticks, warmup 4000. Primary metric: `speed_kmh`.

| option | Δ vs base | Δ% | p | Cohen's d | verdict |
|---|---:|---:|---:|---:|---|
| Peak-hour freight ban — no trucks downtown | 0.76 | 2.3% | 0.0002 | 4.02 | UPGRADE |
| Longer greens — 40% longer cycles citywide | -1.28 | -3.8% | 0.0000 | -6.46 | WORSE |
| Widen Lake Shore Drive — one lane each way | 0.17 | 0.5% | 0.0321 | 1.20 | UPGRADE |
| Widen the Kennedy — one lane each way | 0.06 | 0.2% | 0.3862 | 0.39 | no-op (n.s.) |

## Answer key

- **Peak-hour freight ban — no trucks downtown** (`truck-ban`): UPGRADE, 2.3% on speed_kmh, p=0.0002
- **Longer greens — 40% longer cycles citywide** (`retime-long`): WORSE, -3.8% on speed_kmh, p=0.0000
- **Widen Lake Shore Drive — one lane each way** (`lsd-widen`): statistically real but below the 1% practical floor, 0.5% on speed_kmh, p=0.0321
- **Widen the Kennedy — one lane each way** (`kennedy-widen`): no-op (n.s.), 0.2% on speed_kmh, p=0.3862

## Everything tested

| option | Δ% | p | d | verdict |
|---|---:|---:|---:|---|
| retime-long | -3.8% | 0.0000 | -6.46 | WORSE |
| transit-5 | 2.1% | 0.0000 | 5.56 | UPGRADE |
| truck-ban | 2.3% | 0.0002 | 4.02 | UPGRADE |
| ramp-meter | 1.3% | 0.0009 | 2.86 | UPGRADE |
| retime-short | 2.1% | 0.0021 | 2.38 | UPGRADE |
| lsd-widen | 0.5% | 0.0321 | 1.20 | UPGRADE |
| kennedy-widen | 0.2% | 0.3862 | 0.39 | no-op (n.s.) |
