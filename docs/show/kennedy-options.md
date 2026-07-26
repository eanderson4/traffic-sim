# Kennedy corridor — upgrade options

Baseline `base`, 8 paired seeds, 12000 ticks, warmup 4000. Primary metric: `speed_kmh`.

| option | Δ vs base | Δ% | p | Cohen's d | verdict |
|---|---:|---:|---:|---:|---|
| Peak-hour freight ban — no trucks on the corridor | 1.90 | 3.9% | 0.0000 | 6.17 | UPGRADE |
| Speed harmonisation — 80 km/h posted on the mainline | -1.48 | -3.1% | 0.0000 | -3.71 | WORSE |
| Widen the mainline — one extra lane each way | 0.32 | 0.7% | 0.0890 | 0.70 | no-op (n.s.) |
| Widen the on-ramps — longer, wider merge lanes | -0.13 | -0.3% | 0.3456 | -0.36 | no-op (n.s.) |

## Answer key

- **Peak-hour freight ban — no trucks on the corridor** (`truck-ban`): UPGRADE, 3.9% on speed_kmh, p=0.0000
- **Speed harmonisation — 80 km/h posted on the mainline** (`speed-harmonise`): WORSE, -3.1% on speed_kmh, p=0.0000 — CAUTION: moves -0.6% less vehicle-distance (p=0.0125), so this is not a like-for-like comparison: the network is doing less work, not doing it better
- **Widen the mainline — one extra lane each way** (`mainline-widen`): no-op (n.s.), 0.7% on speed_kmh, p=0.0890
- **Widen the on-ramps — longer, wider merge lanes** (`ramp-widen`): no-op (n.s.), -0.3% on speed_kmh, p=0.3456

## Everything tested

| option | Δ% | p | d | verdict |
|---|---:|---:|---:|---|
| truck-ban | 3.9% | 0.0000 | 6.17 | UPGRADE |
| transit-5 | 2.8% | 0.0000 | 4.33 | UPGRADE |
| speed-harmonise | -3.1% | 0.0000 | -3.71 | WORSE |
| ramp-meter | 1.1% | 0.0022 | 1.67 | UPGRADE |
| mainline-widen | 0.7% | 0.0890 | 0.70 | no-op (n.s.) |
| ramp-widen | -0.3% | 0.3456 | -0.36 | no-op (n.s.) |
| retime | 0.0% | 0.8932 | 0.05 | no-op (n.s.) |
