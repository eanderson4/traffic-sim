# ADR-0037: Runtime signal control — design sketch (deferred implementation)

- **Status:** PROPOSED — design only, implementation deferred to its own
  milestone.
- **Date:** 2026-07-30
- **Amends:** ADR-0011 (signals), ADR-0006 §4 (contract: one new verb or
  intent class) when implemented.
- **Context:** ADR-0036 (adaptive routing), the retime what-if brackets
  (`data/runs/whatif-chi-retime/`).

## Why this is queued

Signals are pure fixed-time programs: `SignalProgram` compiled from the
network file, state a pure function of tick (`engine/signal.go`,
`phaseAt`). The retime brackets on chi-loop-urban prove timing is a
first-order lever at network scale — uniform greens ×0.66 bought +1.6 %
mean speed (p=0.046) and ×1.25 cost −5.3 % (p=0.0009) — and real traffic
management is RESPONSIVE, not uniform: actuated phases, adaptive
corridors, gating during oversaturation. The seed-42 drain run
(`data/runs/drain-chi-base/`, full network stop by minute 65) is the kind
of condition a real engineer meters signals against.

The enforcement path is designed for this swap (`engine/signal.go:36-39`):
a controller replacing the fixed-time derivation of the per-approach state
(`sigState`) leaves `sigGate`, clearance, and permissive/protected
discipline untouched. What does not exist is the COMMAND CHANNEL:
`GrantSignal` in the attach handshake is a stub — no subject, payload, or
engine hook consumes a signal command.

## Design (to be validated at implementation time)

**Channel: a director verb, not an intent axis.** The verb machinery on
`ts.{run}.ctl.verb.{controller_id}` already provides request/reply,
idempotency (request_id), logging on `ts.{run}.log.verb`, and verbatim
re-enqueue by replay — the four properties a signal command needs, and
the same ones an intent axis would have to rebuild. Signal commands are
network-level acts (a phase change is not a vehicle act), so the
director-grant verb plane is the natural home; a new verb kind
(`signal_set` / `signal_phase`), payload: signal id, commanded state or
phase index, optional hold-until tick.

**Engine side: commanded-state derivation.** `sigState` gains a third
source beside fixed-time and clearance: a commanded override table
(signal id → commanded phase/state + expiry), replaced wholesale by each
verb and DETERMINISTIC because the verb log fixes the applied tick
(ADR-0008's intent precedent). The override must be keyframed (new
keyframe section, same lowest-version rule as v5/v6) — a held phase is
state that decides what vehicles do, and a seek that loses it diverges.
Enforcement, clearance windows, and the permissive yield model do not
change: a controller can hold red or extend green, it cannot make green
mean "enter a box you cannot exit".

**Safety rails (the reason this is not just a verb):** an unrailed phase
command can starve a movement for good (a red that never lifts — the
pathology ADR-0034's escape deliberately does not rescue). Commands
therefore carry a maximum hold (default one cycle length; explicit
hold-until bounded), after which the program resumes its fixed-time
derivation. Starvation is observable (per-approach wait already in
metrics), and a held phase past its bound lapses loudly — a logged
event, not a silent fallback.

**Controller side:** out of scope here. A minimal actuated controller
(gap-out on detector occupancy from `state.snap` / metrics) is the
reference client the implementation milestone ships to prove the channel;
network-level adaptive control is a research program of its own.

## Open questions for the implementation milestone

- Verb granularity: whole-program switch vs per-phase advance vs
  per-approach state. Per-phase with hold-until is the current lean —
  expressive enough for actuation, coarse enough to keep the starvation
  rails simple.
- Keyframe/CRC scope: override table per signal id, plus the
  deterministic resume point of the underlying fixed-time program
  (derived from tick — no extra state, verify at implementation).
- Interaction with epoch routing (ADR-0036): a held phase changes lane
  travel times, which reroutes flow onto the held movement's alternatives;
  whether signal commands should poke `ttEMA` directly is deferred —
  first guess is no, let the dwell samples speak.
- Contract versioning: new verb kind on an existing subject family —
  additive, same formal-supersession line as the 2026-07-24 addenda, but
  the asyncapi addition and the ADR land in the same reviewed commit.
