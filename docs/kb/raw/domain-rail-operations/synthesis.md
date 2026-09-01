# Synthesis: Rail Operations

> Researched: 2026-08-24 | Git HEAD: 2bc98de | Status: complete
> Feeds a future rail-operations ADR (unnumbered at time of writing) and the
> pending domain-multimodal-transit topic. This synthesis recommends; the ADR
> decides.

## Summary

The question: how should traffic-sim model the CTA 'L' — grade-separated
heavy rail with 2–8-car consists (14.6 m cars, up to ~117 m), 10–30 s station
dwells, ~3–8 min Loop headways, and flat-junction interlockings at Tower 18 /
Tower 12 — given a road engine with point-fronted vehicles, a junction
right-of-way guardrail, fixed-time plus runtime-commandable signals, and a
replay-verified intent log?

The trace found rail currently dies in three places (extract keeps only
`highway` ways — scripts/chicago/extract.py:52-55, scripts/overpass-lean.py:43;
importer's motor-class filter — engine/netimport/netimport.go:83-88), and that
**zero rail lanes exist in any shipped .net.xml**, so enabling rail is a
pipeline decision at three seams, not a rediscovery problem.

The long-vehicle spike — the topic's #1 technical risk — came back **mostly
reassuring: four real findings in the spike itself (L1–L4), plus a
strand-escape interaction (L5)**. The engine's gap math is
length-correct everywhere it matters for following (leaderAt across lane
chains, injection clearance, cross-overlap detection, exit-room rule); a 117 m
consist drives, spawns, crosses junctions, and records like any vehicle. The
findings: **(L1)** the 100 m leader-sight bound (engine.go:575-577)
silently assumes vehicle extent ≪ sight distance — a >100 m consist can have
its rear in danger while its front is invisible, blinding IDM *and* the
ADR-0025 safety gate alike; **(L2)** junction box occupancy is
front-bumper-based, so a consist's tail is invisible to conflicting movements
once its front exits the box — the length-aware exit-room rule
(rightofway.go:331) covers entry, not exit; **(L3)** the exit-room walk must
be re-verified on real rail geometry — a junction whose exit chain
structurally fails the 119 m need (terminal throats, pocket tracks) seals a
train at the interlocking forever (implementation.md §1.3); **(L4)** metrics
attribution is front-bumper — lane-interval occupancy books the consist's
full footprint on one lane (engine/metrics.go:493) while spanned neighbors
read empty; trip-level measures are unaffected, and the disposition
(distribute occupancy across spanned lanes vs. document the front-bumper
convention) is DEFERRED to the rail ADR (implementation.md §1.6). Separately,
**(L5)** the strand escape
(ADR-0034) will remove a scheduled train that holds >300 s at head-of-lane
facing a capacity-blocked interlocking — *unless the hold rides the signal
plane* (a red aspect is never a strand trigger, gridlock.go:166-171), which
turns a hazard into a design argument: rail holds belong in signals, not in
throttles (strand analysis: implementation.md §2).

The architectural conclusion: the kernel already owns the rail *physics*
approximation (the ADR-0025 gate IS SUMO's Rail car-follow model in spirit —
safe-stop envelope), and the ADR-0037 `signal_set` verb already owns the seam
for rail *control* (interlocking = external controller issuing bounded aspect
holds; strand-safe, replay-safe, lapse-bounded). What the kernel lacks —
driveways, schedule priority, blocks, stations — can all begin life as
controller-plane policy plus scenario-side authored data, with kernel changes
(L1, maybe L2) made small and separately reviewable. Metra commuter rail and
grade crossings defer cleanly: the L is grade-separated, and the
gate-as-signal-phase pattern (FRA ≥20 s warning, 49 CFR 234.225) slots into
the same signal plane whenever it becomes worth modeling.

## Source Files

- [Implementation: codebase trace, the long-vehicle spike, strand/rowGate/signal/keyframe analysis](./implementation.md)
- [Prior art: SUMO rail, OSRD, OpenTrack, MATSim transit](./competitors.md)
- [Standards & patterns: blocks, CBTC, headway theory, interlockings, CTA facts, FRA, GTFS](./standards-and-patterns.md)

## Key Findings → Decision Candidates

### D1. Import rail lanes with a class mask — three-seam pipeline change

**Choice:** Keep railway ways at extract (add `railway` to the kept-tag rules
at extract.py:52-55 and overpass-lean.py:43), let netconvert emit rail edges
(default typemap already does), and teach the importer to keep `rail`-class
lanes **emitting a per-lane class mask** (new optional `NetLane` field;
`engine/netfile.go:48-79` has none today) rather than dropping them via
`motorClasses` (netimport.go:83-88).
**Why:** The skip was a deliberate non-goal, not an accident (report fields
`SkippedEdges` document it). Every downstream consumer (routing,
pickSuccessor, lateral policy, spawn origins) assumes one undifferentiated
lane set; importing rail without the mask lets cars route onto track and
trains onto road. The mask is also the multimodal-transit topic's bus-lane
mechanism — build it once, here.
**Trade-off:** Network-format change (additive, omitempty — v1 readers ignore
unknown fields, but confirm before ratifying); the mask needs kernel
enforcement points (routing filter, lateral suppression by class) that are
new code in routing.go and mobil.go; importer identity hash changes (the
import-city.sh REPO_REV covers netimport.go, so provenance handles itself).
**Field context:** netconvert's rail conventions (distinct edges per track,
`bidi` superposed edges, rail_signal/rail_crossing junction types) are the
input contract we inherit unchanged ([SUMO Railways](https://sumo.dlr.de/docs/Simulation/Railways.html)).
The two-pass stop-override netconvert flow (import-city.sh:73-99) is
unaffected.

### D2. Train = a long vehicle class on rail-masked lanes

**Choice:** One `VehicleType` per consist class (e.g. `l8`: Length 117 m,
S0 ≈ 5, T ≈ 1.0, A ≈ 1.0, B ≈ 1.1, V0 ≈ 24 — a/b **unverified**, open
question §OQ1), registered in the three hardcoded type registries
(serve/main.go:154, demosrv/params.go:28, simrun/main.go:66). Trains spawn
via director verbs at terminal portal lanes, routed by the existing Route
axis to the far terminal.
**Why:** The vehicle model carries length per *type* (vehicle.go:8-17), so
consists need no per-vehicle state; typeIdx already rides CRC and keyframes.
Director spawn gives schedule control and replay identity for free.
**Trade-off:** F∈[0.8,1.3] driver heterogeneity draws apply to trains too
(spawn.go:285-290, director.go:381-385) — clamp to F=1 for rail types or
accept that only the IDM-fallback path mis-speeds (the rail controller
drives by intent regardless). DirectorSpawnHoldTicks=600 (60 s) can drop a
scheduled train at a blocked terminal — the rail scheduler must re-issue or
the window needs a class-aware bump.
**Field context:** CTA consists are married-pair multiples of 14.6 m up to
8 cars/117 m, platform-limited ([CTA Rail Car Maintenance Plan](https://data.ntsb.gov/Docket/Document/docBLOB?ID=40423963&FileExtension=.PDF&FileName=Rail%20Car%20Maintenance-Master.PDF)).

### D3. Fix the >100 m sight assumption before any train rolls (L1)

**Choice:** Make the leader walk's sight bound consist-aware — e.g. continue
past maxSightM while the found leader's rear (dist + l.S − l.Type.Length)
is within the follower's braking envelope — or raise maxSightM for rail
types. Small, separately reviewable kernel change with a fixture.
**Why:** engine.go:567-577 documents the bound as calibrated to car braking
(72 m + margin). A 117 m leader can be collision-relevant with its front
beyond 100 m; both IDM and the safety gate (engine.go:780-813) read
`e.leader(v)` and go blind together. Without this, a coasting train meets
its leader at emergency decel instead of service braking — survivable, but
not 'L'-shaped, and CrossOverlaps/Collisions would say so.
**Trade-off:** Any change here touches every CRC fixture that exercises the
walk bound (the change must be behavior-identical for car-only networks —
it is, if the bound only extends where a long leader is found; verify by
bit-identity). Alternatively: declare it controller-solved (the rail
controller maintains its own lookahead via observation frames) and defer —
honest option, but the fallback/coasting path stays sharp-edged.
**Field context:** SUMO documents the same class of issue from the other
side: speed limit = min over *all edges the train occupies*, reversal needs
the whole consist past the switch ([Railways docs](https://sumo.dlr.de/docs/Simulation/Railways.html)).

### D4. Rail driving = external controller; IDM-as-block is fallback with documented limits

**Choice:** A rail controller service (NATS) drives each train by 4-axis
intents: station-approach braking via reqAcc profiles (NOT cruise-0 — the
servo stops within one tick at up to −9 m/s², vehicle.go:125-134), block
discipline as controller-maintained separation, dwell execution, schedule
adherence. The kernel IDM with rail parameters is the disconnected-fallback
only, documented as *not* a block system.
**Why:** Zero driving logic in the kernel is the architecture (ADR-0008);
intents are recorded and replay re-applies them bit-exactly (ADR-0005) — a
controller-side rail layer inherits the whole replay story with no record-
plane change. The kernel's safety gate = SUMO's `carFollowModel="Rail"`
safe-stop envelope ([Railways docs](https://sumo.dlr.de/docs/Simulation/Railways.html));
CTA's actual ATC is continuous cab signaling with overspeed enforcement
([NTSB RAB1102](https://jonroma.net/media/rail/accident/usa/ntsb/RAB1102.pdf)),
i.e. closer to our gate than to wayside blocks — the approximation is honest
about which CTA behavior it mimics.
**Trade-off:** Headway fidelity now depends on controller quality; the
observation/control loop at 10 Hz with batched intents (ADR-0026) is proven
for cars, and dozens of trains is a trivial fleet beside 10⁴ cars. IDM
equilibrium following (gap ≈ s0+vT) is documented as non-rail in the
fallback path.
**Field context:** Precedent: the default-driver fleet and the ADR-0037
sigctl controller are already external, replay-safe control services.

### D5. Interlocking = signal program + external schedule-priority controller (NOT rowGate)

**Choice:** Compile Tower 18/12 plants as junctions whose internal lanes bind
to signal programs; an interlocking controller watches occupancy/approach
over observation frames and issues `signal_set` holds by route table +
schedule priority (SUMO's railSignalConstraints pattern — predecessor
constraints as data — implemented controller-side).
**Why:** The signal plane is strand-safe (red never triggers ADR-0034's
escape — gridlock.go:166-171), replay-safe (ADR-0037 verbs are logged,
keyframed v7, CRC-folded), lapse-bounded (300 s chain clamp = the tower-
goes-dark fail-safe), and enforcement already composes with the box checks
(green never enters a blocked box = home-signal absolute semantics).
rowGate fails the mapping: static classes, no advance route locking, ID
tie-break instead of timetable priority, foes compiled within one junction
only, RowStop forcing full stops at clear signals (implementation.md §3).
**Trade-off:** Route/approach locking becomes *policy in an external
process* — the kernel will not stop a controller that releases two
conflicting routes at once (it will only keep each train out of the
occupied box, one tick at a time). That's the same trust split as vehicle
controllers; the record plane makes mis-dispatch replayable and countable.
Schedule-priority tables are new scenario-side data.
**Field context:** Tower 12 is literally hand-dispatched for schedule
priority today ([chicago-l.org tower12](https://www.chicago-l.org/operations/towers/tower12.html));
approach locking (don't cancel a cleared route once a train is inside
braking distance) is controller policy per the AREMA/glossary definitions
([Metrolinx GI / AREMA 1.1.1](https://assets.metrolinx.com/image/upload/v1737995080/Documents/Engineering/General_Instructions_GI.pdf),
[Pachl glossary](http://www.joernpachl.de/glossary.htm)).

### D6. Blocks = synthetic one-lane junctions at signal locations (format-compatible)

**Choice:** Place synthetic junction nodes at block boundaries (CTA ATC
block points) on imported rail edges, binding the block entrance to a
program; the interlocking/block controller drives aspects. Start with a
coarse block layout (station-to-station + interlocking approaches) and
refine only if validation demands.
**Why:** Signals bind only to internal lanes of junctions today
(netimport.go:402-436); SUMO's own placement guidance is "split the edge or
type the node" ([Railways docs](https://sumo.dlr.de/docs/Simulation/Railways.html))
— the same constraint, solved the same way. Synthetic junctions stay inside
network-format v1; no new engine concept.
**Trade-off:** Each synthetic junction creates internal lanes that trigger
the whole rowGate/sigGate/box-walk machinery — mostly harmless on a
single-successor rail lane (gateTarget walks through, foes empty) but the
import spike must verify no pathological gating (and ADR-0038 consolidation
must not eat them — check `ConsolidatedSlivers` on the rail import).
Actual CTA block spacing is unpublished (open question §OQ2) — the coarse
layout is also a data-availability concession.
**Field context:** The Loop runs at 2–4× its block-limited minimum headway
(derivation in standards-and-patterns.md §4), so coarse blocks are unlikely
to bind validation — dwells and interlockings will.

### D7. Stations/dwells = authored, GTFS-derived scenario layer

**Choice:** Stations are scenario-side data (lane id + s + dwell program),
derived from CTA GTFS stop_times/frequencies
([CTA GTFS](https://www.transitchicago.com/developers/gtfs/)); the rail
controller executes dwells as stop-hold-depart profiles. Published run times
and headways become the validation corpus (a `gtfs-check.py` against ADR-0014
trip records, the SUMO scheduleStats.py pattern).
**Why:** The network format has no stop concept and shouldn't grow one for a
scheduling concern (stations are demand, like ADR-0021's OD anchors, not
geometry). GTFS is the authoritative public source for both the scenario
and the ground truth — same recipe-not-file posture as ADR-0009.
**Trade-off:** Platform-edge alignment (which s on which lane) needs an
authoring tool or a GTFS-shapes-to-lane matcher; dwells derived as
departure−arrival deltas where GTFS gives both fields, else the 10–30 s
planning band (§OQ3).
**Field context:** MATSim proves the line→route→stop-offsets pattern at
scale (competitors.md §4); CTA measures its own rail OTP against scheduled
headway ([Service Standards, May 2023](https://www.transitchicago.com/file.aspx?DocumentId=8041)).

### D8. Defer: grade crossings (gate-as-signal-phase), Metra, reversal/turnback geometry

**Choice:** Document, don't build. Gate = red phase on road approaches,
triggered by approach detection with ≥20 s minimum warning (49 CFR 234.225),
gate motion ≥3 s after flashers (234.223), release after the consist's tail
clears ([FRA/FHWA Crossing Handbook](https://railroads.dot.gov/sites/fra.dot.gov/files/2024-10/GXHandbook2019FRAFHWA.pdf));
SUMO's tuned `rail_crossing` state machine (time-gap 15 s, opening-delay 3 s)
is the reference tuning ([Railways docs](https://sumo.dlr.de/docs/Simulation/Railways.html)).
Structural prerequisite noted: today the crossing junction evaporates with
the skipped rail lanes (DroppedConnections, netimport.go:294-297), so D1
must land first and the crossing node must survive compilation.
**Why defer:** the L is grade-separated; the first rail milestone answers
Loop questions, not Metra/UP crossing questions. Train reversal/turnback
(SUMO needs the whole consist past the switch, plus turn-around connections)
is likewise deferred — Loop ops are a circuit, terminals are portals.

## Compare / Contrast vs Prior Art

| Aspect | This project (proposed layer) | SUMO rail | OpenTrack / OSRD |
|---|---|---|---|
| Primary spacing | ADR-0025 safe-braking gate + controller block policy | carFollowModel="Rail" safe-stop + rail_signal block guard | Full route/block interlocking logic |
| Blocks | Synthetic junctions + signal_set (D6), coarse | First-class rail_signal driveways with flank/bidi sets | First-class, timetable-integrated |
| Schedule priority | Controller-side constraint table (D5) | railSignalConstraints as data, generateRailSignalConstraints.py | Native (timetable is the input) |
| Long trains | Point front + implicit tail; L1 sight fix needed; L2 box-tail caveat | Length-aware occupancy/speed/reversal, documented | Native |
| Stations/dwell | GTFS-derived authored layer (D7) | Stop lists on trips, scheduleStats.py validation | Timetable DB, punctuality analytics |
| Grade crossing | Deferred; signal-phase pattern (D8) | rail_crossing type with tuned parameters | Native |
| Road interaction | **Same lane graph, same tick, same metrics** — the whole point | Co-simulation in one net, weaker on road control APIs | None |
| Replay/verification | Bit-exact replay of intents incl. signal verbs | Deterministic given inputs; no record plane | Deterministic; no record plane |
| Scale of effort | Approximation layer over a road engine | Mature rail feature set in a road sim | Dedicated 40-person products |

## Open Questions

1. **L2 fix or document?** The box-exit tail invisibility (rightofway.go:277-286)
   is loud (collision counters catch it) but real. Options: extend foe
   occupancy to count vehicles whose *rear* still spans the internal lane
   (needs S − Length ≥ 0 overlap test — cheap), or document as
   exit-room-rule-covered and measure. Decide with a Tower 18 fixture.
2. **L3 exit-room walk on real rail geometry** — verify no Loop junction's
   exit chain fails need=119 m structurally (terminal throats, pocket
   tracks); a structural failure is a permanent seal *and* a strand trigger.
3. **OQ1: CTA traction/braking specs** (a, b per series) — unpublished as
   far as this pass found; IDM params would start from generic metro values.
4. **OQ2: CTA block layout on the Loop** — unpublished; coarse layout
   proposed (D6). Possibly recoverable from the ATC-replacement procurement
   docs.
5. **OQ3: dwell distributions** — compute from CTA GTFS stop_times where
   arrival and departure both exist; else 10–30 s band.
6. **OQ4: netconvert edge shape for the two-track elevated** — confirm
   single-lane edges per track on import (the lateral-policy safety of
   trains depends on it, implementation.md §1.5).
7. **Strand escape on rail lanes** — even with signal-plane holds, should
   `StrandAfterS` be per-class or rail-lane-exempt? A disabled rail network
   (power outage scenario) strands legitimately; the escape's "loud, bounded,
   countable" doctrine wants a rail equivalent rather than silence.
8. Does the ADR-0039 density gate interact with scheduled rail spawns?
   (Directive deferral clock exists; a density-capped network could defer a
   *train*. Probably: exempt scheduled-transit directives or accept and
   count — note for the ADR-0039 iteration, not this one.)

## Connections

- **domain-multimodal-transit** (PENDING): owns the class-mask layer map and
  person-weighted metrics; D1's mask is shared infrastructure — whoever
  lands first should build it for both.
- **domain-bus-operations** (PENDING): bus lanes via the same class mask;
  TSP over the ADR-0037 pattern is the same external-signal-verb precedent
  as D5's interlocking controller; dwell models share D7's GTFS derivation.
- **domain-demand-modeling** (PENDING): CTA GTFS ridership/schedules as a
  calibration source; rail as a mode-share coupling term.
- **arch-road-graph-model** (researched): lane-as-atom multigraph, compiled
  conflict sets — D1/D6 extend the compiled file without breaking the atom;
  synthetic junctions are the format-compatible block mechanism.
- **integration-osm-extraction** (researched): the extract/typemap/importer
  provenance chain; D1's three-seam change rides the existing recipe-not-file
  and importer-identity-hash machinery (import-city.sh:117).
- **domain-traffic-flow-models** (researched): IDM parameterization for rail
  classes; the safe-stop envelope (ADR-0025) as Krauss-family car-following —
  the rail CFM mapping belongs in that topic's car-following taxonomy.
- **ADR-0037** (accepted): the signal_set verb is D5/D8's entire control
  channel; the interlocking controller is a second external signal client
  after sigctl.
- **ADR-0034** (accepted): the strand-escape doctrine ("a red light is never
  a trigger") shapes the D5 design; §OQ7 proposes a rail-aware look at the
  escape rather than an exemption by default.
- **ADR-0038** (accepted): sliver consolidation runs on any rail import —
  verify synthetic block junctions and switch connectors survive the 5 m
  threshold (consolidate.go:52).
