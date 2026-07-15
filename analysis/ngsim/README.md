# NGSIM I-80 wave analysis

First real-data exercise for the observability layer: download an NGSIM I-80
congested window, compute Edie's generalized q/k/u on an x–t grid, render the
speed heatmap, and measure the backward congestion-wave speed.

Method background: `docs/kb/raw/domain-trajectory-datasets/` (Edie definitions,
x–t heatmaps, ASM characteristic smoothing). Data license: CC BY-SA 3.0 (FHWA
NGSIM). We commit scripts, never data — `data/` is gitignored.

## Usage

```sh
./download-i80.sh 1700-1715        # → data/ngsim/i80-1700-1715.csv (~94 MB)
go build -o ngsim-xt .
./ngsim-xt -in ../../data/ngsim/i80-1700-1715.csv \
  -png ../../data/ngsim/i80-1700-1715-xt.png \
  -field ../../data/ngsim/i80-1700-1715-field.csv
```

Periods: `1600-1615` (lighter), `1700-1715`, `1715-1730` (congested).
`ngsim-xt -h` lists the knobs (grid size, lane filter, color scale).

## What it computes

- **Edie (1963) per x–t cell**: q = Σdistance/|A|, k = Σtime/|A|, u = q/k, from
  consecutive 0.1 s sample pairs assigned by segment midpoint. Integrals, not
  derivatives — robust to NGSIM's known position noise.
- **Heatmap PNG**: x up (direction of travel), t right, sequential blue ramp,
  dark = congested. Stop-and-go waves appear as dark stripes sloping down-right.
- **Wave speed**: scans candidate speeds c; the c whose characteristic lines
  x = x₀ + c·t minimize within-line variance of the congested speed field is the
  dominant wave speed (the Adaptive Smoothing Method insight, inverted into a
  measurement).

## Result (2026-07-15, first run)

I-80 Emeryville, 2005-04-13 5:00–5:15 pm, lanes 1–6, 1,965 vehicles,
1.52 M sample pairs, 1791 ft section, grid 25 ft × 3 s:

```
dominant congestion wave speed: -16.5 ft/s = -18.1 km/h = -11.2 mph
```

Published analyses of this same dataset report shockwaves at ≈ 11 mph
(~18 km/h), and the general literature gives −15…−20 km/h as the universal
backward wave speed — our independent Edie implementation reproduces it.

## Toward the engine

The Edie computation here is the prototype of the engine's observability
service: the same math must later consume simulated vehicle trajectories (over
NATS) so that real data and sim output flow through identical analytics
("one Edie implementation, two consumers" — see
`docs/kb/raw/domain-trajectory-datasets/synthesis.md`). When the sim can
reproduce a −15…−20 km/h wave from car-following dynamics alone, this tool is
the referee.
