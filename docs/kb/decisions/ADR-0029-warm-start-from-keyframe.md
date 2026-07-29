# ADR-0029: Warm-start a run from a keyframe

- Status: Accepted for phase 1 (below); decisions 1 and 3 remain Proposed
- Date: 2026-07-28
- Extends: [ADR-0005](ADR-0005-time-model.md) §5 (keyframes)
- Touches: `engine/keyframe.go` (a future keyframe format — see the note
  on numbering under Decision 1), the scenario schema
  (`initial_state`), and run identity. Not the NATS message contracts.

## Context

Every Chicago run starts from an empty network and spends its first 20–30
simulated minutes filling. At 54,000 ticks that fill is a third of the run and
roughly 20 minutes of the ~60 minutes of wallclock, and it is *the same fill
every time*: the same demand program pouring into the same network. An A/B
batch of six runs pays for it six times to measure a difference that only
exists after it is over.

Worse, it interacts with the measurement. The fill-curve confound (mechanism
10 in [Silent Fidelity Failures](../articles/concepts/silent-fidelity-failures.md))
is exactly this: a window that includes any of the fill reports how fast the
network fills rather than what it settles at, and the shorter the run the more
of it is fill.

The obvious fix is to start already loaded. The question is whether that is a
new mechanism or one the engine already has.

## What already exists

It largely exists. ADR-0005 §5 keyframes are full-state snapshots that carry
everything needed to resume **bit-exactly**: per-vehicle lane, position,
speed, follower state, per-vehicle PCG RNG state and draw count, the
persistent controller axes (cruise, held turn, signals, route), the spawner's
per-origin schedule and pending RNG, and the director queue. `engine/replay`
already seeks to the nearest keyframe ≤ a target tick and re-simulates
forward. The codec, the CRC and the chunking (ADR-0015) are all in place.

So warm start is not "add snapshots". It is "let a scenario BEGIN at a
keyframe instead of at tick 0".

## The constraint that decides the design

A keyframe stores a vehicle's **lane index**, not its lane id:

```go
// engine/keyframe.go
if int(laneIdx) >= len(e.Net.Lanes) {
        return nil, fmt.Errorf("keyframe: vehicle %d lane index %d out of range", id, laneIdx)
}
... Lane: e.Net.Lanes[laneIdx]
```

The only check is a range check. Load a keyframe against a network whose lane
array differs — one extra lane inserted anywhere near the front — and every
vehicle after that point is placed on a **different, valid, wrong** lane. No
error is raised, the run proceeds, and the output is garbage that looks
exactly like output.

That matters because the highest-value use of warm start is precisely the
case it currently cannot serve: giving every run of a lane-widening A/B the
same loaded starting state. `mknetvariant.py --add-lane` changes the lane
array, so the base keyframe is silently invalid for the widened run.

The vehicle's *destination* is not affected — `Vehicle.Route` is a
**destination lane id** string (`engine/intent.go`), and routing is
destination-based via memoized next-hop tables, not a fixed path. So a
warm-started vehicle re-routes correctly through a changed network. Only its
current position is unportable.

## Decision

Three parts, in order of dependency.

**1. A future keyframe format: identify lanes by id, not index.** Write a lane-id
string table once in the header and have vehicles, origins and directives
reference it. On load, resolve ids against the network being loaded and fail
loudly on any id that does not exist. This converts the silent misplacement
above into an error, and it makes a keyframe portable to any network that
still contains the lanes the vehicles are standing on — which includes every
lane-*adding* variant, and correctly excludes lane-removing ones.

> **On the version number.** This decision originally called itself "format
> v4", then "v5". Both are now taken: v4 is the destination fields and **v5 is
> ADR-0034's stuck timer**, shipped before this decision was implemented. The
> lane-id format is therefore whatever the next free version is at the time it
> lands, and this ADR deliberately stops naming a number — a reserved number
> that ships to someone else is worse than no number.

**2. `initial_state:` in the scenario schema.** A path to a keyframe. The run
begins at that keyframe's tick with that state, and the demand program
continues from there. Absent, behaviour is exactly as today.

**3. The keyframe's content hash enters run identity.** This is the M11
lesson repeated: the seed-in-hash identity bug shipped because something that
determined the output was not part of the thing that identified it. Two runs
with identical scenarios and identical seeds but different warm-start states
are different runs and must not be able to claim the same identity.

## Consequences

Good: an A/B batch pays the fill once instead of once per run — on a six-run
batch at 54,000 ticks that is roughly two hours of wallclock back. Runs start
not merely from the same seed but from the *identical* loaded state, which
removes the fill's contribution to between-run variance entirely rather than
cancelling it statistically. And the fill-curve confound stops being a
measurement hazard, because there is no fill inside the measured window.

The costs are real and one of them is conceptual rather than technical:

- **A warm-started A/B measures a different question.** Both runs starting
  from a state produced under the BASE network means the widened run begins
  out of equilibrium and relaxes toward a better one. That answers "what
  happens if you widen this road during the peak" — a legitimate and
  arguably more interesting question — but it is **not** "what is the
  equilibrium under a widened road". The two must never be quoted
  interchangeably, and any result from a warm-started batch has to say which
  it is. Measuring the equilibrium question still requires each run to fill
  under its own network.
- **A keyframe is a large binary blob bound to a network.** It is derived
  data, so it belongs in gitignored `data/`, which means an archived result
  cannot be fully reproduced from the repo alone unless the keyframe is
  reproducible — it is, from the base run, but only if that run's identity is
  recorded.
- **The lane-id format is a migration.** Existing keyframes in recordings must
  still load; the reader keeps both paths and only the writer moves.
- **Restored stats start fresh** (already true for replay). A warm-started
  run's `totals` therefore describe the measured window, not the vehicle's
  whole life — trips that began before the keyframe have no entry tick, and
  `completed_trips` will undercount relative to a cold run. The report must
  say so rather than let the two be compared.

## Alternatives rejected

**Keep the cold fill and just measure a later window.** What we do now. It is
correct and it is why this is a performance ADR rather than a correctness
one — but it costs the fill on every run of every batch, and the cost scales
with exactly the thing we want more of (longer runs, more runs).

**Synthesise a loaded state directly** — place N vehicles at plausible
positions and speeds without simulating. Rejected: a hand-placed state is not
on the model's own attractor. Car-following state, lane-change history and
queue structure would all be invented, the network would spend its first
minutes relaxing out of an unphysical configuration, and that relaxation
would be indistinguishable from the effect under test.

**A short "pre-load" demand burst** to fill faster. Rejected: it changes the
demand program, so the state reached is the equilibrium of a different
scenario, and the burst's own queues persist well into the measured window.

## Phase 1 as built (2026-07-28)

Same-network warm start only. Decisions 1 (lane ids) and 3
(keyframe hash in run identity) are NOT implemented; decision 2 is
implemented as command-line flags rather than a scenario field.

- `serve -state-out FILE -state-at TICK` dumps the engine's full state at
  that tick and **continues** the run. `serve -state-in FILE` begins the run
  at that state's tick. `-ticks` stays absolute.
- The silent-misplacement hazard is closed by a **sidecar**, `FILE.meta.json`,
  not by the keyframe format: FNV-1a 64 over the lane count and every lane
  id, length-prefixed, **in lane-index order** (never map iteration, which
  would vary per process and turn the guard into a coin flip). Load recomputes
  it from the network the spec builds, before any vehicle is placed, and
  refuses on mismatch naming what differs. A missing sidecar is refused, not
  assumed fine. So a lane-widening A/B still cannot share a warm start — but
  it now fails loudly instead of producing plausible garbage, which is the
  part that could not wait.

  > **What the sidecar does NOT cover (2026-07-29, review).** It fingerprints
  > the network, the vehicle types, the seed and the scenario hash — but not
  > `engine.Params`. `serve` applies `-safety-decel` (default 6) and
  > `-density` after the state is loaded, and neither is in the scenario
  > hash, so saving a state under `-safety-decel 4` and warm-starting without
  > the flag runs different physics from the restored tick with no warning.
  > That is the same default-trap shape this phase hard-refuses for seeds and
  > networks, and it is inconsistent to refuse those two and stay silent
  > here. Not fixed in phase 1 because the fix is a params fingerprint plus
  > an override flag — the same shape as `-state-reseed`, and worth doing
  > once rather than twice. Tracked; until then, a warm start is only as
  > trustworthy as the operator's memory of the flags.
- **`-state-in` refuses `-driver`.** The contract plane is not part of the
  restored state. A warm restore builds a fresh `Contract` — empty claims,
  empty `byVehicle`, empty `hold`, empty `announced` — and `AfterStep`
  publishes each controller's observation BEFORE announcing the vehicles it
  holds no claim on. So on the first resumed tick every restored vehicle is
  unclaimed *and* has no hold-last intent to bridge with (ADR-0008 §6): it
  gets the default, where the cold run at that tick had the driver's. One
  tick of different inputs, against a decision whose entire claim is
  bit-exact continuation. Announcing earlier does not close it — claiming is
  a request/reply round trip, so no within-tick ordering guarantees the claim
  lands before the step. The fix is to persist and reconstruct contract state
  (claims, hold-last, sequence numbers), which is phase 2 alongside the
  director's position. `-driver=false` warm-starts a controller-free run,
  which IS bit-exact because both sides then use the default every tick.
  Caught in review; `-driver` defaults to **true**, so this was the
  combination a user got by not passing a flag.
- **The run registry records the real start tick.** `Registry.Start` used to
  hard-code `StartedTick: 0`, so a warm-started run published a false start
  tick on the metadata plane while the engine, the attach hello and the
  initial keyframe all said the restored tick. It now takes the tick from
  `e.Tick`, authoritative for cold and warm alike. Also caught in review, and
  worth noting as a class: three of this phase's defects were a warm start
  quietly disagreeing with something that was still assuming tick 0.
- **`-state-in` refuses `-store`.** The record plane anchors a recording with
  a keyframe at the run's first tick, and replay anchors on a keyframe at
  tick 0 (`player.go`: "no keyframe ≤ target tick 0"). A warm-started run's
  first keyframe is at the warm-start tick, so its recording is unopenable.
  Bending the record plane to accept a nonzero keyframe floor is the phase-2
  question; warm start ships for unrecorded debugging runs, which is the
  requirement. Pinned by
  `natsio.TestWarmStartRecordHasNoTickZeroKeyframe` — if the floor ever
  lifts, that test fails and points here.
- **`-state-in` refuses a scenario with demand parts.** The director cannot
  yet resume exactly where a saved state left off, and the error is a silent
  duplicate rather than a loud failure: a cold director at tick N has already
  sent every arrival up to N+`Lead`, the engine holds those as accepted
  directives that the keyframe restores, but `fastForward` skips only
  arrivals ≤ N — so a warm director resumes INSIDE that window and re-sends
  it, and the contract's request-id dedup map is per-process and starts
  empty. Those arrivals spawn twice. Fast-forwarding to N+`Lead` instead is
  not the fix: verbs for arrivals near the window edge may still be in
  flight when the dump fires, so it trades duplicates for losses. Restoring
  it exactly means reconciling per flow against the restored queue, which is
  phase 2's problem. Until then the combination is refused rather than
  approximated — warm start is for runs whose spawning is built in.
- The **demand director fast-forwards** on warm start (`demand.Config.StartTick`).
  Reachable today only through the **library seam** (`demand.Config.StartTick`)
  and its tests: serve refuses the combination above, and the standalone
  `demand-director` now refuses a nonzero `-start-tick` outright for the same
  double-spawn defect. No command-line path reaches it. The machinery is kept
  because it is the correct first half of the phase-2 fix. Arrivals before the warm-start tick
  already happened — they are in the restored state — and the sampler skips
  them by *drawing* them, so every flow lands on exactly the position the
  cold run held and the program from that tick on is identical. Without it
  the first snapshot drains the whole backlog as one burst of already-expired
  verbs. Zero (a cold run) skips nothing and is byte-identical to before.
- **`-state-in` refuses a seed the state was not saved under**, unless
  `-state-reseed` says the mismatch is intended. The restored engine draws
  every future stream from the RUN's seed — new vehicles derive their RNG
  from `spec.Seed` and the demand director samples from it — so a mismatch
  splices one deterministic program's history onto another's continuation,
  which is precisely what the bit-exact criterion below rules out. It is a
  default trap rather than a hypothetical: `-seed` defaults to 1, so
  warm-starting a state saved under any other seed and merely *forgetting*
  the flag yields a plausible run assembled from two programs. A printed
  note is not enough for a failure whose entire signature is that the run
  looks fine. Reseeding on purpose stays available; it has to be said out
  loud. Pinned by `TestValidateStateFlags`.
- Bit-exactness is the acceptance criterion, and the oracle is the existing
  rolling CRC chain: `engine.TestWarmStartRoundTripIsBitExact` and
  `natsio.TestRunLiveWarmStartIsBitExact` cut a run at tick N, restore, and
  require the CRC chain to match an uninterrupted run tick for tick.
- Still true from Consequences: restored stats start fresh, so a warm-started
  run's `completed_trips` undercounts a cold run's and the two must not be
  quoted together; and a state file is derived data bound to one network.

## See also

- [ADR-0005](ADR-0005-time-model.md) — keyframes and replay
- [ADR-0015](ADR-0015-keyframe-chunking.md) — keyframe chunking over NATS
- [Silent Fidelity Failures](../articles/concepts/silent-fidelity-failures.md)
  — mechanism 10, measuring a network that is still filling
