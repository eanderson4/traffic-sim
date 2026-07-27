## Review — ADR-0026 batched intents (TSIB v1)

Read the full patch plus the surrounding repo. Codec, demux, expansion ordering, whole-batch vs per-record drop rules, the driver split/route-divert logic, the fallback advertisement, and the contract doc all check out against each other and against ADR-0026. Contract-change discipline is satisfied (ADR present, asyncapi bumped to 2.5.0, KB index updated). No map-iteration, wall-clock, or RNG issues in any engine-side sampled path; the record plane is untouched, so replay determinism is preserved by construction and `TestBatchOnOffParity` pins it. The verbatim-case header lookup (`msg.Header[headerIntentEncoding]`, bus.go:317) is correct for nats.go's non-canonicalizing `Header`, and the driver-side tests exercise it over a real broker, so the assumption is test-pinned, not just asserted.

**Blockers: none.**

**Should-fix**

1. `contracts/asyncapi.yaml:1237` + `engine/natsio/bus.go:342` — interop trap: the hello advertisement names `v2` as an accepted encoding, but a controller that expresses that literally (`intent_encoding: v2`) has every intent dropped as *unknown* by the demux. Worse, that same message **works against a pre-TSIB engine** (old engines ignore headers), so upgrading the engine breaks such a producer — an inversion of the additive-change promise. Either accept `"v2"` as a synonym for absent in the demux switch (one case), or add one sentence to the `intent_encodings` field description stating that the `v2` capability token is expressed on the wire by header *absence*, never as a header value. The channel description implies it, but the enum is where an external implementer will look.

**Nits**

2. `engine/natsio/driver/driver.go:421,435` — publish errors still swallowed silently, and in batch mode one failed `PublishMsg` now loses up to 20,000 intents for the tick (hold-last heals ≤2 ticks, then zero/default fleet-wide) instead of one. This is the exact failure shape the new cross-topic-concerns KB entry records as "recorded, NOT yet fixed" for `publishObs`; fine to defer per the triage bar, but the driver intent path deserves inclusion when that item is fixed — consider adding it to the KB entry's scope.
3. `engine/natsio/driver_test.go:1328,1442` — stale comments: both say the production pace is 3 ms ("at 3 ms with sub-ms work it is zero", "as at 3 ms") but `prodPace` is 10 ms; BENCHMARKS.md documents the deliberate 10 ms choice. Comment-only drift inside shipped test rationale.
4. ADR-0026 and the `ControlIntentBatch` title say "880,024 B payload + header" — 880,024 (24 + 44×20,000) already *includes* the header. Wording only; the code's length math is right.

**Questions**

5. `engine/natsio/tsib.go:126` — an empty batch (count 0) is structurally valid, counted as an accepted batch, and expands to nothing; the driver never sends one (`len(batch) > 0` gate, driver.go). Intentional as a keep-alive-shaped allowance, or should M-next tighten it? Documented in `TSIBView` (`count minimum 0`), so consistent as shipped — flagging only so it's a decision, not an accident.

REVIEW-COMPLETE
