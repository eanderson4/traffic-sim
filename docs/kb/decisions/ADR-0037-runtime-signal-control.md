# ADR-0037: Runtime signal control — design sketch (deferred implementation)

- **Status:** ACCEPTED — milestone 1 implemented 2026-07-31 (verb channel,
  engine override derivation, starvation rails, TSKF v7); milestone 2
  implemented 2026-08-04 (reference actuated controller, bracket runner).
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

## Addendum (2026-07-31, milestone 1 implementation notes)

Implemented as designed: the `signal_set` director verb, the engine-side
commanded-state derivation, and the starvation rails. Where the design
left latitude, the implementation chose:

- **Verb granularity: per-phase with a hold DURATION, not an absolute
  hold-until tick.** The payload is `{signal, phase, hold_ticks}`; the
  hold runs from the verb's applied tick, which the record fixes, so a
  duration needs no clock agreement between controller and run and
  replays verbatim. `hold_ticks` omitted/0 means one cycle of the
  commanded program — the natural bounded quantum of the program's own
  making, NOT a seamless resume: one cycle after the command began, the
  fixed-time schedule is back at the phase that was active WHEN THE
  COMMAND BEGAN, which is not necessarily the COMMANDED phase, so a lapse
  can jump from the held phase to an unrelated schedule phase; the
  resume follows the schedule from the lapse tick and the clearance
  retention covers the transition (an earlier draft of this note claimed
  the default was seamless — wrong, caught in review). Whole-program
  switches and per-approach states remain unbuilt, per the design's lean.
- **Over-long holds CLAMP, not reject.** The bound is
  `engine.SignalHoldMaxSeconds` = 300 sim-seconds (the gridlock escape's
  own `StrandAfterS` horizon: a hold past it would starve a movement
  longer than the model will stay silently stopped anywhere else),
  compiled onto the run's tick grid at acceptance
  (`signalHoldMaxTicks`, the routeEpochTicks dt-derivation idiom — 3000
  ticks at the validated 100 ms tick). The rail exists to bound a wedged
  controller's blast radius automatically; a rejection would depend on the
  controller reading its reply. The record plane logs the EFFECTIVE hold
  in ticks, so what was enforced is on the record.
- **One derivation point.** `sigPhaseAt` (engine/signal.go) is the single
  place commanded control enters: all three enforcement predicates
  (`sigState`, `sigPermissive`, `sigInClearance`) read the phase in force
  through it, so a hold changes WHICH state string is read and nothing
  about enforcement — `sigGate`, the clearance window, the box checks, and
  the permissive-yield model are untouched. With an empty override table
  `sigPhaseAt` reduces to the fixed-time `phaseAtElapsed` and the byte
  stream is the pre-ADR-0037 one. An ended hold is RETAINED for the
  program's clearance window (not enforced) so `sigInClearance`'s
  onset−1 lookback is still answered for the ticks the hold covered — for
  BOTH ways a hold ends: lapse at the bound, and supersession by a newer
  verb, whose entry keeps its phase with `until` truncated to the
  replacement tick (round-2 review: the first cut dropped a superseded
  entry, so a held amber replaced by red could miss clearance and a held
  green replaced by red could inherit a spurious one from the unrelated
  schedule). The table is therefore a short chronological history per
  program, not a single slot.
- **The clearance window opens at the DISPLAYED transition, lapse
  included** (round-3 review). Round 2 made supersession correct because
  the replacement supplies a fresh onset; lapse had the symmetric bug —
  `sigInClearance` measured the window from the fixed schedule's
  historical phase onset, which may be long past at the moment the light
  actually changed. A held amber lapsing into an already-red schedule got
  its clearance shortened or skipped (could stop a legally committed
  vehicle); a held green lapsing onto a schedule whose red began before
  the hold could inherit a spurious clearance from the schedule's earlier
  amber. The fix computes a DISPLAYED onset (`sigSpanOnset` +
  `sigDisplayedOnset`, signal.go): an override's applied tick while held,
  the LATER of the fixed onset and the most recent override end after a
  lapse, with contiguous same-phase spans merged (a lapse back into the
  held phase is no display change). The no-override path reduces to
  `phaseAtElapsed` exactly — every pre-existing fixture CRC is untouched.
- **Contract-boundary hardening** (round-4 review). Two wire-validation
  gaps closed in `handleVerb`, both covering every verb kind at the one
  shared checkpoint. (1) `request_id` is bounded at 65,535 BYTES: the
  TSKF codec length-prefixes it with a u16 (the director spawn queue
  since v3, the override table since v7), so an over-long id would have
  marshaled a truncated length next to the full string and made the
  keyframe unreadable — the spawn path shared the exposure, and the
  single shared check now covers both. (2) `phase` is presence-checked
  for signal_set: the request field is a `*int` because JSON cannot
  distinguish omission from phase 0, and an accidental default would
  silently command phase 0 — omitted is rejected ("missing phase"),
  explicit 0 works. Spawn verbs are byte-identical (phase is never
  serialized there). Round 5 hardened the record plane to match: a logged
  signal_set record encodes `signal_idx`/`phase` WITHOUT omitempty
  (explicit presence — 0 is legitimate for both, and a phase-0 record
  would otherwise be indistinguishable from a malformed phase-less one),
  and decode rejects a signal_set record missing either, same hard-error
  idiom as the unknown-kind rejection. The spawn record shape is
  untouched. The signal_set record shape change is free: no released
  recording contains one — the record encoding is versioned by the run
  record, and this ADR's implementation has not passed the gate yet.
- **The clearance merge is lane-STATE-aware across override boundaries**
  (round-5 review). Round 3's displayed-onset walk merged contiguous spans
  by phase INDEX; two different phases can show the same state for an
  approach (SUMO splits a red across consecutive phases), and across a
  supersession or lapse between two such phases the index changed while
  the lane's display did not — closing the clearance window early and
  recapturing an amber-committed vehicle into the stale-red wall. The merge
  now compares the SigState the phase yields FOR THE QUERYING LANE
  (`sigPhaseState`; `sigDisplayedOnset` takes the lane). Crucially, the
  state-aware merge applies ONLY across override boundaries
  (`sigSpanOnset` reports whether a span's onset is one): fixed-to-fixed
  phase onsets keep the pre-ADR-0037 index-based onset even between
  same-state phases, because merging those changed 15,002 clearance
  evaluations per two cycles on chi-loop-urban — the legacy fixed-time
  behavior is preserved for byte-identity with every recording, not
  endorsed. Differential-verified: the new path matches the pre-change
  formula at every lane, every tick of two full cycles on both
  testdata/signal-4way and chi-loop-urban.
- **The starvation bound is cumulative across a hold CHAIN** (round-6
  review). As first built, every superseding verb reset the hold horizon,
  so a client loop re-commanding the same phase every N ticks could
  starve a movement indefinitely — the advertised 300 s never landed —
  and a renewal landing exactly at the old hold's end suppressed the
  lapse event. Now: an uninterrupted run of same-phase holds on one
  program (each superseding the last with no fixed-time gap) is ONE chain
  anchored at its first hold's applied tick (`chainStart`, keyframed and
  CRC-folded — it decides future behavior); a renewal's effective end
  clamps to `chainStart + bound`. A different phase, or any fixed-time
  gap, starts a new chain (a real control action; an interrupted
  starvation). At the bound the chain lapses and exactly ONE lapse event
  fires, whether the chain ended by expiry or by a renewal arriving at
  the bound — such a renewal is accepted and recorded as applied (the
  command reached the engine) but installs no override; the rail declined
  to extend the hold. The lapse event's `since` reports the chain start,
  so the event spans the whole starved interval.
- **The request-id bound moved into the kernel** (round 6):
  `engine.MaxRequestIDBytes` (65,535 bytes) is enforced by both
  `EnqueueSpawn` and `EnqueueSignal`, so every caller — not just the NATS
  contract layer — is guarded against corrupting the u16-length-prefixed
  keyframe sections; the contract layer keeps its wire-facing check
  against the same constant. The asyncapi schema drops `maxLength`
  (JSON Schema counts CHARACTERS; the engine enforces UTF-8 BYTES) and
  documents the byte limit with the engine check authoritative — no
  engine behavior change.
- **The resume point needs no state** (the open question, verified): the
  fixed-time program is a pure function of the tick, so a lapse is just
  the override table stopping answering. Lapses are EVENTS
  (`Engine.LapsedSignals`, per-tick): the run loop edge-logs each one AND
  records it on `ts.{run}.log.event` as a `signal_lapse` ContractEvent
  (signal, phase, [since, until), request id) — the pause/resume
  precedent: dead-time metadata the replayer ignores and re-derives from
  keyframed state — so a recording contains the evidence the rail fired
  (round-2 review: the first cut logged to process stderr only).
- **Grant: the director grant.** Signal commands ride the director verb
  plane as designed; a controller holding only the (previously stub)
  signal grant is rejected, same as for spawn. What the signal grant
  becomes is milestone-2 contract work.
- **Keyframe: TSKF v7, lowest-version rule.** The override table (program
  index, phase, since, until, request id — network Signals order, each
  program's history chronological) is appended after the director section
  and written ONLY while an override is held (or in clearance retention);
  a run without signal verbs marshals
  byte-identical to before. v7 also gives the header flags word its first
  bit (`keyframeFlagAdaptive`): through v6 the ADR-0036 lane section's
  presence rode the version number (v6 was written only flag-on); at v7 a
  flag-OFF run can hold an override, so presence needed an explicit bit.
  The per-vehicle `laneEntryTick` rides the same condition as the lane
  section (round-2 review: the first cut wrote it at every v7, so a
  flag-off→flag-on restore would have imported dwell clocks from a run
  with no ttEMA — now a payload without the adaptive state seeds every
  dwell clock at the restore tick, v7-flag-off included, exactly the
  ADR-0036 "no evidence yet" migration).
  Versions ≤ 6 read exactly as before. The rolling CRC folds the override
  (behavior fields only — the request id is audit trail) LAST, and only
  while the table is non-empty.
- **Contract: asyncapi 2.7.0, additive.** The logged verb carries an
  omitted-when-spawn `verb` discriminator, so every pre-ADR-0037
  recording decodes unchanged (all-spawn); replay, the player, bake, and
  the log cursor all re-enqueue both kinds.
- **Deliberately not in milestone 1:** the reference actuated controller
  (milestone 2); any `ttEMA` poke (the design's first guess — let the
  dwell samples speak — stands); and state-plane publication of held
  phases. `ts.{run}.state.sig` publishes the program TABLE and clients
  derive light states from the tick, so a held phase is not reflected in
  what a viz client derives — a display-only gap, owned by milestone 2's
  client work (the record plane and CRCs are exact regardless).
  **Controller-facing feedback is part of that M2 design work** (round-7
  review): a renewal declined at the chain bound is wire-invisible to the
  controller today — the verb answers `accepted: true`, the lapse (and
  the declined extension) is visible only on the record plane
  (`signal_lapse` event), and `state.sig` never shows holds. How a
  controller learns that its hold lapsed or its renewal was declined
  (lapse visibility, override state on the live plane) must be designed
  in M2 BEFORE client work binds the current silence.
- **Round-7 hardening.** Duplicate request-id detection now also scans
  the keyframed override history (active and retention-held entries), so
  a warm restore — where the contract layer's per-process reply cache is
  empty — cannot re-apply a previously accepted command and reset
  since/until; the window is the retention sweep, the same bounded
  semantics as EnqueueSpawn's live-queue check. The signal_set record
  decode also rejects a missing/zero `hold_ticks` (the encoder always
  writes the effective hold ≥ 1, so 0 means the field was never written —
  previously such a record silently replayed with the one-cycle default).
  Doc alignment: the `signal_lapse` event's `since`/`until` descriptions
  now state the chain semantics (chain start; the chain's clamped end),
  replacing the wrong `since + effective hold_ticks` formula.
- **Recorded and deferred from the round-7 review** (hardening/nits, not
  blockers): keyframe-reader chronology/disjointness hardening for the
  override history (the restore validates ranges but not cross-entry
  ordering — a corrupt payload is a tooling fault, not a wire input);
  unknown-flags tolerance in the TSKF header for v≥7 (a future flags bit
  would be ignored, not rejected — forward-compat posture to decide when
  a second bit exists); the kernel's sigNew-only dedup window vs the
  director queue's live-queue dedup (spawn and signal have slightly
  different duplicate-detection horizons by construction — closed for
  signal in round 7's history scan; the note stands for the retention
  window's edge); and the
  retroactive-replay edge of the request-id length bound (a pre-bound
  local-harness recording with an over-long id would now fail replay
  loudly — no such recording exists on the wire path, where the check
  always ran).
- **Round-8 hardening.** (1) `sigSpanOnset`'s offset-wrap guard (a fixed
  phase onset wrapping before tick 0 in an offset program) returned a
  fictitious zero onset without checking whether a hold lapsed inside
  that first partial phase — silently denying clearance; it now reports
  the lapse boundary. (2) The record now carries the EFFECTIVE hold:
  `appliedSig`/`SigLog`/the logged verb are stamped with the
  chain-clamped `until − since` at install, and a renewal declined at
  the chain bound is recorded with an explicit `hold_ticks: 0` —
  "accepted, enforced nothing", the lapse event carrying the enforced
  truth. The codec keeps the round-7 presence discipline exactly:
  `hold_ticks` is a pointer in the decode struct, a MISSING field is a
  hard decode error, and an explicit 0 is the legitimate declined marker.
  Replay is unaffected: an installed hold's recorded effective value
  re-clamps to the same `until` from the same chain state, and a declined
  renewal's outcome depends on chain state, not the recorded number.
  (3–4) asyncapi corrections only: `state.sig` now states that during a
  signal_set hold the tick-derived state is WRONG (fixed-time-only until
  the M2 feedback story lands), and the request_id idempotency promise is
  reworded to the true bounded window (wire-layer reply cache for the
  live session; kernel dedup over the same-boundary buffer plus the
  keyframed override history / spawn live queue; undetectable after a
  lapse leaves retention — ids must not be deliberately reused).
- **Rail scope, stated explicitly** (round-8 review): the starvation
  bound is a PER-PHASE hold bound — it bounds how long one commanded
  phase can be held against the program, cumulatively across renewals.
  A controller ALTERNATING two phases that both show red for one approach
  can starve that MOVEMENT indefinitely without tripping the rail; that
  is controller policy, not a rail defect. Per-movement service under
  legitimate phase alternation is the controller's responsibility —
  forcing a movement green against an actively alternating controller
  would be a far more invasive engine behavior (the engine would be
  overriding control, not bounding it).
  `TestSignalSetAlternatingPhasesNotClamped` codifies the intended
  behavior.
- **The third end-of-chain case** (round-6 enumerated expiry and
  renewal-at-bound; round-8 adds this one): a supersede to a DIFFERENT
  phase applied exactly at `chainStart + bound` fires NO `signal_lapse`.
  Mechanics: the old entry's clamped until IS the bound tick, so the
  truncation is a no-op and the newest-entry lapse check sees the new
  hold's until in the future. This is by design: the starvation ended AT
  the advertised bound through the controller's own superseding verb,
  which is itself on the record — nothing is silent; the audit trail is
  the verb log.
- **Round-9 hardening.** Two verbs for the same program applying at one
  tick boundary: the later supersedes the earlier before it governs a
  tick, and the earlier entry was dropped as an empty interval while its
  logged verb kept a positive hold_ticks — the record claimed an
  effective hold that governed zero ticks, violating the round-8
  "exactly what the engine enforced" semantics. The drop now stamps the
  earlier verb's record-plane copies (appliedSig AND SigLog — separate
  slices) to hold 0, the accepted-but-enforced-nothing marker, consistent
  with the declined-renewal semantics; the wire-level record shows the
  same 0 (pinned in TestSignalVerbValidation). The request-id side needs
  no code: a dropped entry's id leaves the keyframed history immediately,
  which is inside the bounded-dedup-window contract language of round 8.
  Also: the sigNew buffer's not-keyframed invariant (a marshal between
  EnqueueSignal and the next Step would drop a buffered verb; every
  marshal site runs with the buffer drained) is now documented at the
  field declaration as the dirNew precedent — resolving the round-9
  question as an emergent invariant, no assert.
- **Recorded and deferred from the round-9 review** (nits/questions):
  `engine/metrics_test.go` TestKernelReplayRederivation hand-mirrors
  Replay()'s enqueue loop and was not extended with the Signals loop —
  harmless today (its fixture issues no signal verbs), but it must be
  kept in sync or switched to calling Replay when a signal-verb fixture
  lands there; and VerbRequestView's `required` relaxed to [verb,
  request_id] is the intended trade (additive direction; per-kind
  validation stays server-side, documented per field).
- **Recorded and deferred from the round-8 review** (nits):
  `sigPhaseAt`'s elapsed return is dead at the sigState/sigPermissive
  call sites (only sigInClearance's onset walk consumes elapsed);
  of the two round-4 contract rejections, only the over-long
  request_id check fires before the dedup store; the missing-phase
  rejection is stored, so a retry of that id replays the cached
  rejection with duplicate: true (harmless — reusing an id for a
  corrected payload already violates the idempotency-key contract);
  the run.go lapse-event
  `json.Marshal` error is deliberately unchecked (ContractEvent has no
  unmarshalable fields — same posture as the existing event
  marshals); and LoggedVerbView's per-kind required fields are documented
  in prose because draft-07 if/then is not the file's established style.
- **Verified:** verb accept/reject/idempotency over the wire; two
  identical verb-scheduled runs CRC-identical; keyframe restore mid-hold
  continues CRC-exact through the lapse, both routing-flag regimes; a
  no-verb run marshals at the pre-change version (v2 flag-off, v6
  flag-on); a held red past its bound lapses (logged) and the fixed-time
  program resumes at the tick-correct phase; a held green still cannot
  enter a blocked box; the scripted proof fixture (hold one approach red
  → its queue grows, the cross movement flows → lapse → the queue
  discharges) passes; replay/player/bake reproduce a signal-verb run
  CRC-exact from the record, including a seek onto a mid-hold keyframe.
- **Recorded and deferred from the round-10 review** (committed in M1;
  triaged under the AGENTS.md triage-bar addendum of 2026-08-04):
  Sol's round-10 blocker — a same-boundary B-then-A verb pair lets the
  final A inherit a fresh chainStart because chain ancestry is resolved
  before the empty B interval is dropped, so repeated same-tick
  multi-verb sequences can bypass the cumulative rail — is deferred:
  no client exists today, the sequence is outside any controller we
  plan to run, and the failure mode is a hold continuing as commanded
  (not wrong simulation behavior, nondeterminism, or record
  corruption); M2's controller must not rely on the rail covering it,
  and the fix (resolve chain ancestry after dropping empty
  intermediates) is queued for the M2 hardening pass. Also deferred:
  a same-phase renewal one tick after each lapse starts a fresh chain
  (Kimi question — adversarial-controller policy per the rail-scope
  paragraph, like alternating phases); math.Round(300/dt) can overshoot
  the 300-s bound by one grid step when dt does not divide 300 (Sol);
  requestID is excluded from the CRC fold, so warm-restore
  idempotency-state divergence is invisible to the determinism oracle
  (Sol); signal_lapse fires at every hold end, not only rail-clamped
  bounds — an M2 event-volume design input, since a per-cycle command
  cadence would emit a per-cycle record event (Kimi); lapse surfacing
  is confined to RunLive — engine.Run and the cmd harnesses never read
  SigLapses (Kimi); and nits (markSignalVerbUnenforced's SigLog scan
  lacks an early exit; ContractEvent Since/Until use omitempty while
  Phase uses a pointer). Kimi's round-10 review found no blockers and
  confirmed the earlier deferred lists match the shipped code.

## Addendum (2026-08-04, milestone 2 implementation notes — reference actuated controller)

Milestone 2 ships the reference client the design sketch promised ("a
minimal actuated controller (gap-out on detector occupancy from
`state.snap` / metrics)"): `engine/natsio/sigctl` (library), `cmd/sigctl`
(standalone), and `serve -sigctl` (in-process embed behind the attach
barrier, the demand director's pattern — but every command rides the
signal_set verb channel, which is what the client exists to prove).

- **Detectors are virtual stop-line loops, and the ADR's "occupancy from
  state.snap" needed one honest refinement.** The TSSF snapshot carries
  (id, x, y, angle, class) per vehicle — no lane, no s — so approach
  presence is read the way a physical detector reads it: a fixed zone in
  space (default 25 m radius around each signal link's stop line, the
  start of the signal-bound internal lane's centerline). Dynamic
  structure (programs, phases, link→lane binding) arrives over the wire
  (TSSG/TSSF); only the geometry comes from the network file, which is
  static scenario content. Programs with < 2 movement phases or no usable
  shapes are left on fixed time. Presence is binned through a coarse
  spatial grid (50 m cells) — city-scale snapshot fan-out is fine.
- **Control law: gap-out with min-green, max-green-on-call, and the rail
  as the backstop.** Phases are classified movement vs transition:
  anything containing an amber char (even mixed green/amber — one exists
  in chi-loop-urban, program 2169310) or with no green at all is a
  transition, never a serve or hold target. Per program per decision
  (cadence 20 ticks): an uncontested serving phase is extended freely
  (renew when the hold runs below 30 ticks); a conflicting call (≥ 1
  waiting vehicle on another candidate phase's detectors) caps the
  extension at 200 ticks (max-green-on-call — past it the call is served
  regardless of the gap); a gap with a call waiting switches after a
  100-tick minimum green; idle junctions (no presence anywhere) stay on
  fixed time and cost no verbs. A switch is never a jump: the controller
  walks the program's table order to the target, commanding every
  intermediate phase at its NATURAL duration — the program's yellow/
  all-red intervals are actually simulated (free clearance time would
  inflate the measurement; caught in M2 review round 1, fixed before the
  full bracket ran). Same-phase renewals accumulate into the kernel's
  starvation chain, so the 300 s chain bound remains the hard backstop,
  and the controller predicts it from its own verb history — it never
  issues a renewal the rail would decline (the chain-contiguity boundary
  matches the kernel's exactly: `applied > holdUntil` starts a new
  chain; equality continues it). Min-green measures the PHASE's age:
  renewals do not restart it (the kernel's displayed-onset merging gives
  the same answer — same-state spans merge across renewal boundaries, so
  controller and kernel agree on when the displayed phase began). Two defects
  were found and fixed BY MEASUREMENT on the smoke bracket before these
  notes were written: detectors that reached past the stop line never
  gapped out (zero switches; renewals held toward the chain bound —
  detectors now count the approach side only, using the lane's direction
  vector), and unconditional renewals then suppressed cross calls
  entirely (fixed by the max-green-on-call cap). Measured on chi-loop
  smoke brackets (9,000 ticks, warmup 6,000, seed 1000): naive gap-out
  cost ~40 % network speed vs fixed time; approach-side detectors →
  −24 %; + max-green-on-call → +15.9 % speed, +15.2 % completions —
  and M2 review round 1 then showed that number was INFLATED by
  transition-skipping (free clearance time); the final controller
  (transition walk, seamless steps) measures **+10 % speed, +6 %
  completions** on the same smoke (one seed, smoke length — direction,
  not significance; the full bracket is the measurement).
- **Cadence decision (round-10 deferral a).** Verbs are issued only on a
  decision — a renewal when the running hold has < 30 ticks left, a
  switch when one is warranted — never one-verb-per-cycle. A switch is a
  supersede and fires NO `signal_lapse`; a lapse fires only when a hold
  is allowed to end (gap-out release, or the chain bound). So record-plane
  event volume is bounded by phase SERVICES, not by cadence: ≤ 1 lapse
  per service per program. Measured on the final smoke run: 30,432 verbs
  over 933 controlled programs in 9,000 ticks (~1.7 M verbs projected at
  the full 54k-tick bracket — small next to the intent log's millions).
- **Feedback gap (round-10 deferral b) — resolved for the reference
  client by self-tracking, documented for the contract.** The controller
  keeps its own command history per program, so the enforced phase, the
  chain start, and the exact decline point of the chain bound are all
  COMPUTED locally (estimated()): it never issues a renewal the rail
  would decline, because it can see the decline coming. This is exact
  while the controller is the only commander of a program — the reference
  deployment shape. What the contract would need for anything stronger
  (multi-controller programs, an external monitor, a viz showing held
  phases): an override-state echo — minimally, the effective
  (program, phase, until) published on the state plane (state.sig or a
  sibling), or a per-verb effective-hold field in the verb reply.
  PROPOSED, not implemented: the contract is not changed unilaterally.
- **The controller never relies on the rail to arbitrate same-boundary
  multi-verb sequences (round-10 deferral c):** one verb per program per
  decision, and decisions are cadence-gated, so two verbs for one program
  never share a boundary.
- **Bracket:** `scripts/sigctl-bracket.py` pairs the scenario
  (`data/scenarios/chi-loop-urban-half-base`, the ADR-0036 survivable
  bracket's, 54k ticks, seeds 1000+) fixed-time vs `-sigctl`, reusing
  whatif.py's run_one/fidelity gates/paired statistics (whatif's pod
  model cannot express arms that differ by a serve flag). Smoke-validated
  at 9,000 ticks (both arms clean, fidelity gates pass, control
  diverges). Full command in the addendum below.
- **Verified:** decision core unit tests (take-control, renew,
  max-green-on-call switch, rail-cap switch, gap-out, min-green,
  transition-phase and idle leave-alone, tie-break); estimated-phase
  tracking; the live integration test (`TestControllerLive`, signal-4way
  crop): attach, 10/10 verbs accepted, deterministic request ids, and at
  least one held tick diverging from the fixed-time schedule. Smoke
  bracket (seed 1000, 9,000 ticks, warmup 6,000): both arms exit clean
  through the fidelity gates; the actuated arm's controller attached,
  commanded 933 programs, 30,432 verbs, 0 rejections. The full bracket
  command (runner validated by the smoke):

      python3 scripts/sigctl-bracket.py \
        --scenario data/scenarios/chi-loop-urban-half-base \
        --seeds 4 --ticks 54000 --warmup 6000 --jobs 4 \
        --serve ./serve --out data/runs/sigctl-bracket.json

## Addendum (2026-08-04, M2 review round 1 — fixes and deferrals)

Fixed before the full bracket ran (the pre-fix bracket was discarded and
re-run against this controller):

- **(A) Phase model and the transition walk.** Any phase containing an
  amber char — even mixed green/amber — is now a transition phase: never
  a serve or hold target (previously the controller could hold a
  clearance phase for a full 100-tick hold). Switches walk the program's
  table order to the target, commanding intermediate phases at their
  natural durations, so yellow/all-red intervals are simulated rather
  than skipped. Direct movement-to-movement jumps were free clearance
  time and would have inflated the bracket.
- **(B) Min-green measures the phase's age.** `phaseSince` moves only
  when the commanded phase changes; renewals no longer restart the clock
  (with hold == min-green == 100 a gap after a renewal could never gap
  out before the hold lapsed). The kernel's displayed-onset merging
  (same-state spans merge across renewal boundaries) makes its answer
  identical — an intentional, documented equivalence, not a divergence.
- **(C) Table acquisition.** The controller requests the program table
  at attach (ADR-0016 request/reply on its own inbox), and the chunk
  accumulator enforces 1..n sequence per generation — a partial or
  out-of-order generation is dropped and the previously installed table
  keeps serving (previously a mid-generation attach could install a
  partial table).
- **(D) Chain-contiguity boundary matches the kernel exactly:**
  `applied > holdUntil` starts a new chain; equality continues it (the
  kernel renews iff `last.until >= applied`). The equality case is
  unit-tested.
- **(E) Detector scan extent derives from the radius** (50 m cells;
  radius > 50 scans wider) — a larger -detect-radius can no longer
  silently miss detectors.
- **(F) Timing precondition documented, not fixed:** self-tracking
  assumes next-boundary application (`applied := tick + 1`); nothing on
  the wire guarantees it, and at pace 0 (the bracket's mode) the applied
  tick can lag a boundary. The skew is conservative — a predicted
  holdUntil that ends early falls back to fixed-time derivation while
  the kernel still holds, and the uncontested branch may re-command the
  schedule's phase over its own live hold; self-correcting noise and
  latency, never free green. The same skew applies to WALK STEPS
  (round-3 note): a lagging drain can supersede a yellow early or zero
  a short transition step via a shared drain, so seamless clearance is
  the next-boundary ideal, not a pace-0 guarantee. The shipped smoke
  numbers include this skew. The VerbReply applied-tick/effective-hold
  echo remains the recorded contract proposal — this review is its
  concrete argument.
- **Recorded and deferred (round-1 nits/questions):** the send loop's
  map iteration order varies cross-program verb order on the wire (sim
  state unaffected; sorting would make the client deterministic given
  identical snapshots); the 15 s snapshot-silence watchdog cannot
  distinguish run-ended from a capacity-gate pause and never fires if
  the run dies before the first snapshot; LoadGeom panics on a negative
  tlLink in a hand-crafted netfile (standalone-only input, netimport
  never emits one); live_run_test.go is a loaded-CI flake candidate
  (900-tick run at a 1 ms floor); CadenceTicks: 1 in the hello versus
  the 20-tick decision cadence is intended — the handshake cadence feeds
  only drive-claim hold bridging, which this client never uses; and the
  open measurement question: control-drain cost at 933-program scale
  under pace 0 (the ~1.7 M-verb projection is the only unmeasured number
  in the M2 addendum; the full bracket run answers it empirically).

## Addendum (2026-08-04, M2 review round 2 — fixes and deferrals)

Fixed before the bracket restart:

- **(1) Seamless walks.** The round-1 walk stepped transition phases at
  natural durations, but decisions were cadence-gated (20 ticks) and the
  keep-condition required ≥ 30 ticks of hold left, so every short
  transition step LAPSED mid-walk into ~10 ticks of fixed-time
  fallthrough — which could re-green the movement being vacated, then
  jump red with no live clearance, plus a lapse event per step. Now the
  cadence gate lifts while a walk is in progress (dueNow), and each walk
  step's verb is issued exactly when the current step's hold has one
  tick left, so it applies at the expiry boundary: from switch decision
  to target arrival every tick is under a commanded phase — zero
  fallthrough, zero mid-walk lapses (TestWalkSeamless pins it, movement
  → yellow → all-red → target with natural durations).
- **(2) Attach means controlling.** Ready now blocks until a COMPLETE
  table generation is installed (10 s bound, loud error) — the serve
  embed's start gate can no longer open with the controller controlling
  nothing. A complete empty table (signal-free run) releases normally.
- **(3) Request ids carry the engine-assigned controller id**
  (sigctl-{ctlID}-{program}-{seq}): a controller restarting mid-run can
  never collide with its previous process's run-global dedup entries and
  draw a stale accepted reply for a command that never applied.
- **(4) Deterministic verb order.** The decision loop iterates sorted
  program ids — ProcessControl may drain only a prefix of a tick's
  buffered verbs, so publication order can decide which programs apply
  at which boundary; given identical snapshots the order is now fixed.
- **(5) `estimated()` onset saturates at 0** for offset programs in the
  run's first wrapped phase (elapsed > tick underflowed to ~2^64, and
  min-green could never pass there).
- **(6) Bracket fidelity:** the actuated arm must show signal verbs
  applied (the serve log's sigctl tally) or the run is voided —
  whatif.py's fidelity-gate pattern; a clean exit with zero verbs is the
  silent-no-op failure and would otherwise read as a valid arm.
- **Recorded and deferred (round-2 nits/questions):** onReply rejections
  never roll back the optimistic myPhase/holdUntil (self-corrects at
  holdUntil; the restart-collision half is moot after fix 3); the
  lastDecide == 0 tick-0 carve-out (one doubled decision, harmless);
  candidate movement phases whose green links all lack shapes register
  no presence — partial detector coverage is unlogged (revisit if a
  network import ever ships shapeless signal lanes); whole-table frames
  install without resetting the chunk accumulator (inert — the table is
  static per run); bracket verb BYTES are not run-to-run reproducible
  even at pace 0 (applied ticks can lag a boundary — the documented
  round-1 skew); and cmd/sigctl's missing Ready() call is resolved by
  fix 2's semantics (the standalone relies on the silence watchdog; the
  embed blocks the start gate on the table).

## Addendum (2026-08-04, M2 review round 3 — final fixes and deferrals)

Final fix round before merge:

- **(1) The bracket fidelity gate keys on `accepted`, not `verbs`** —
  Sent is incremented before the publish attempt, so an all-rejected
  arm passed the old gate. The void message names the rejected count.
- **(2) The contested switch consults min-green.** An expired
  max-green-on-call budget no longer walks a green the schedule rotated
  into seconds ago — callSince measures the call's age, not the phase's.
  The gate uses the same saturated-onset phase age as the gap-out
  branch (TestContestedSwitchMinGreen).
- **Recorded and deferred (round 3):** (a) a vehicle marks every
  detector within the radius with no approach/link dedup — clustered
  stop-line points on different phases could let one vehicle fabricate
  multiple or conflicting calls and satisfy SwitchMinQueue alone;
  control-fidelity concern, no evidence it fires on chi-loop-urban
  geometry, candidate fix nearest-detector assignment or per-link
  dedup, deferred pending bracket evidence. (b) Same-phase renewal
  bookkeeping stores applied+hold unclamped while the kernel clamps to
  chainStart+bound — after the final allowed renewal, estimated()
  believes the hold persists up to one extra hold interval past the
  actual lapse; bounded and self-correcting, and canExtend's decline
  prediction was independently verified exact. (c) Seamless clearance
  assumes next-boundary application at pace 0 — drain lag can supersede
  a yellow early or zero it via a shared drain; this is the round-1 F
  skew applied to walk steps (the addendum's F note now names them);
  the real fix is the recorded VerbReply applied-tick/effective-hold
  echo contract proposal. (d) The chunk accumulator doesn't verify a
  generation's declared n or request resync after a gap/count change
  (needs a dropped chunk on local NATS; the table is static per run).
  (e) Nits: walk-past-target after a dropped snapshot mid-walk
  (self-correcting); the takeover phaseSince vs kernel displayed-onset
  agreement claim is stated slightly strong (conservative direction);
  mid-comment prose in TestChainBoundaryEquality; the bracket runner
  docstring overstated whatif.py reuse (run_one is re-implemented
  inline — doc accuracy, fixed in the same pass). (f) Questions for the
  record: the reference client requests the director grant while the
  contract defines a stub GrantSignal — a future contract pass should
  decide whether the signal client defines/exercises the signal grant
  (the verb handler checks director today); and 25 m stop-line
  detectors read only the first queue positions, so a queue held
  upstream by spillback reads as a gap — shared with physical stop-line
  loops, accepted for the reference client.
- **Recorded and deferred from the round-4 (M2 final) review** — triaged
  under the merge-priority directive; Kimi found no blockers and
  confirmed the earlier deferral lists are accurate: candidate demand
  scoring includes links already green in the enforced phase, so
  overlapping phases can turn already-served traffic into a conflicting
  call (unnecessary walks after max-green — control efficiency, Sol);
  detector multi-marking persists (fixture evidence: one position can
  match 4-7 colocated stop-line detectors across phase groups —
  candidate fix remains exclusive nearest-link assignment, Sol);
  pace-0 walk seamlessness restated (the VerbReply echo proposal is
  still the real fix, Sol); `-sigctl` with `-state-in` does not restore
  controller state, so a warm continuation diverges (no config runs
  this; replay re-enqueues recorded verbs and is unaffected — candidate
  fix: refuse the combination at flag parse, Sol); Ready succeeds with
  zero controllable programs when ids/geometry match nothing (Sol);
  accepted>0 is not proof a commanded phase governed a tick (rail
  clamps / same-boundary supersedes can zero effective holds, Sol);
  nits: live-test divergence loop doesn't clamp superseded spans,
  zero-length first shape segment degenerates the approach-side test,
  hold>cadence+renew-below constraint is doc-only (Kimi).
