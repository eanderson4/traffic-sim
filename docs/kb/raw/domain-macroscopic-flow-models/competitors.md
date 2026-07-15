# Competitors: Macroscopic Flow Models

> Source: web research (numerics/implementations agent) | Researched: 2026-07-15
> "Competitors" here = existing macroscopic/mesoscopic simulation engines and codebases
> worth studying or benchmarking against.

## Competitive Landscape

Macroscopic (LWR/CTM/LTM) and mesoscopic (queue-based) engines occupy the
"fast, network-scale, planning-grade" niche between static assignment and microscopic
sims. Every major microscopic simulator has grown a meso mode for scale. Notably:
**no Go or Rust CTM/LTM implementation surfaced in targeted searches** — greenfield
open-source territory for this project.

## SUMO mesoscopic mode (`--mesosim`)

- Model: Eissfeldt (2004) vehicle-based **queue model** — edges split into segments
  (default 100 m), FIFO queues, exit times from state-pair headway parameters:
  `tauff` 1.13 s (free→free), `taufj` 1.13 s, `taujf` 1.73 s, `taujj` 1.4 s.
  Runs "up to 100× faster than the microscopic model" with same inputs/outputs.
  Not supported: actuated signals, E2 detectors, sublane model.
  Source: [SUMO Meso docs](https://sumo.dlr.de/docs/Simulation/Meso.html) (fetched).
- **vs traffic-sim:** the cautionary tale. "Revisiting mesoscopic traffic flow
  simulation in SUMO" ([arXiv:2606.09282](https://arxiv.org/abs/2606.09282), 2026)
  found SUMO-meso (i) does not comply with LWR kinematic wave theory,
  (ii) underestimates congestion — appears later, dissipates earlier than micro-SUMO,
  (iii) mishandles backward-traveling space. Their fix, **LIFT**, is a discrete-time
  **Link Transmission Model** (two parameters; event lists of arrivals and
  backward-traveling "spaces") that matches micro output. Lesson: a fast mode that
  ignores kinematic wave theory produces wrong congestion, exactly what our planner
  game must not do. If we build a preview mode, build LTM, not an ad-hoc queue model.

## MATSim qsim

- Spatial-queue model (Cetin, Burri & Nagel 2003): per-link **storage capacity**
  (length × lanes / vehicle length) + **flow capacity** (max outflow/step); spillback
  when storage fills; **no backward wave speed by default** (known LWR deviation;
  later "holes" mechanism propagates jam waves backward at finite speed — ⚠ verify in
  the [MATSim book](https://library.oapen.org/bitstream/id/859157dd-5478-4089-9fca-b3df7a7a39d4/613715.pdf)).
- **vs traffic-sim:** same lesson as SUMO-meso — storage+flow caps alone ≠ kinematic
  waves. But MATSim's per-link O(1) state design is the scalability pattern to study.

## OTM — Open Traffic Models (Berkeley, Gabriel Gomes)

- Java (97.7%), BSD-3-Clause, py4j Python bindings. Natively implements **CTM, "2Q"
  (two-queue), and Newell** link models, arbitrarily mixable in one simulation; plugin
  architecture for models and controllers. Lineage: TOPL → Aurora → BeATS → OTM
  ([ggomes/otm-sim](https://github.com/ggomes/otm-sim), fetched;
  [ITS Berkeley announcement](https://its.berkeley.edu/news/open-traffic-models-platform-large-scale-hybrid-traffic-simulation-0)).
  Small community (18 stars), sporadic maintenance.
- **vs traffic-sim:** the single most instructive codebase — multiple first-order
  link models behind one interface is exactly the "engine-swappable" shape our
  ADR-0001/0002 boundaries want. Study its link-model interface before designing ours.

## UXsim (Toru Seo)

- Python (+some C++), MIT. Mesoscopic **Newell X-model** + "Incremental Node Model";
  ~1200-line core; DTA, signals, route choice; claims 60k vehicles/city in 30 s, 1M
  vehicles/metro in 40 s. Seo 2025, *JOSS* 10(106):7617;
  [arXiv:2309.17114](https://arxiv.org/pdf/2309.17114); [toruseo/UXsim](https://github.com/toruseo/UXsim) (fetched).
- **vs traffic-sim:** closest in spirit to a from-scratch build; proof that Newell/LTM
  mechanics fit in ~1k LOC. Its Newell-following core doubles as the reference
  controller idea for our validation oracle.

## DTALite / Path4GMNS

- Queue-based mesoscopic DTA (Zhou & Taylor 2014); C++, GPL-3.0, **GMNS network
  format**; Python API via Path4GMNS
  ([asu-trans-ai-lab/DTALite](https://github.com/asu-trans-ai-lab/DTALite), fetched).
- **vs traffic-sim:** GMNS (General Modeling Network Specification) is worth
  evaluating as our road-network interchange format — feeds `arch-road-graph-model`.

## Commercial: Aimsun, PTV Visum SBA, DynusT

- **Aimsun Next meso**: **discrete-event** (events = vehicle arrivals at nodes/section
  ends; nodes are queue servers; simplified Gipps for entry/exit times only; no
  within-section simulation); hybrid meso–micro supported
  ([Aimsun docs](https://docs.aimsun.com/next/22.0.1/UsersManual/MesoDiscreteSimulation.html)).
  Relevant precedent for our `arch-time-model` research: a serious commercial engine
  chose pure DES for meso scale.
- **PTV Visum SBA**: mesoscopic simulation-based assignment inside a macroscopic
  planning tool; proprietary ([PTV training](https://training.ptvgroup.com/en/courses/tr-t0170)).
- **DynusT**: mesoscopic DTA, Anisotropic Mesoscopic Simulation (speed responds to
  downstream density only), Fortran core; VISSIM integration for multi-resolution
  ([DynusT wiki](http://wiki.dynust.net/doku.php?id=multi_resolution_modeling);
  ⚠ AMS citation unverified — check Chiu, Zhou & Song 2010, *TR-B*).

## Small open-source references

| Repo | Language | What |
|---|---|---|
| [patmalcolm91/cell-transmission-model](https://github.com/patmalcolm91/cell-transmission-model) | Python | Clean CTM |
| [yanyueliu/Cell_Transmission_Model_Python](https://github.com/yanyueliu/Cell_Transmission_Model_Python) | Python | CTM reading GMNS networks |
| [DanielePignedoli/CellTransmissionModel](https://github.com/DanielePignedoli/CellTransmissionModel) | Python | CTM with density-evolution plots (x–t heatmap for free) |
| [gcostese/Traffic-flow-simulator](https://github.com/gcostese/Traffic-flow-simulator) | see repo | Godunov LWR **with merge/diverge Riemann solvers** — directly on-topic |
| [hoolheart/ctm_matlab](https://github.com/hoolheart/ctm_matlab) | MATLAB | CTM toolkit |
| LightSim ([arXiv:2602.21852](https://arxiv.org/pdf/2602.21852)) | check repo | Lightweight CTM for signal-control RL |
| [traffic-simulation.de](https://traffic-simulation.de) (Treiber) | JS | ⚠ unverified: interactive ring-road/on-ramp wave demos — the teaching-visual gold standard |

## Positioning Summary

| Engine | Model | Time scheme | Language/License | Lesson for us |
|---|---|---|---|---|
| SUMO meso | Eissfeldt queues | fixed step | C++/EPL | fast-but-wrong congestion if LWR ignored |
| MATSim qsim | spatial queue | fixed step | Java/GPL | O(1)/link scalability pattern |
| OTM | CTM+2Q+Newell | fixed step | Java/BSD-3 | pluggable link-model interface |
| UXsim | Newell X-model | fixed step | Python/MIT | LTM-class engine in ~1k LOC |
| Aimsun meso | queue servers | **discrete-event** | proprietary | DES precedent for meso scale |
| DTALite | queue meso | fixed step | C++/GPL-3 | GMNS network format |
| **traffic-sim (us)** | micro engine + LTM oracle/preview | TBD (ADR-0005) | Go/TS, OSS | fill the Go-LTM gap |
