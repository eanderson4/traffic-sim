# Review triage — PR #2 round (2026-08-04)

Round: Fable only (Sol's `codex` CLI installed but unauthenticated — 401;
round re-run pending `codex login`). Archive:
`docs/kb/raw/reviews/2026-08-04T141629-47ce20a9-manual-*`
(run manually — `external-review.sh` needs GNU `timeout`, fixed on the
machine after the first attempt; the round predates the fix).

**Blockers: none.**

## Should-fix (deferred per AGENTS.md triage bar; fix before phase 3 binds outputs)

1. `harvest.py:127` — select-field rejection detection is a substring match
   on the error body; an unrelated 400 would silently drop `id` from
   `select=` and collapse all works into one `id: None` record. Match
   quoted field names instead.
2. `harvest.py:284` — re-running the harvest overwrites
   `research/first-pull.md`, which now has hand-written sections. Write
   the generated summary elsewhere or refuse when the hand-written
   marker is present.
3. `analyze.py:114-118,300` — `hub` tag tie-breaks at the top-25
   boundary depend on set-iteration order (hash randomization): not
   run-to-run deterministic. Fix: `key=lambda kv: (-kv[1], kv[0])`.

## Nits (ignored unless touched anyway)

- `analyze.py:188` — key-route closing comment is backwards (code correct).
- `splash.ts:10-18` — LA `DemoInfo` hardcoded though `/api/demos` is fetched.
- `filter.py:151` — normalized-title dedup may merge distinct works;
  unaudited, note in phase-2.md.
- Pipeline order: `filter.py` after `anchors.py` silently drops anchor
  flags; add a guard.
- `analyze.py:242` vs `filter.py:171` — null-year sorts as 0 vs 9999.

## Questions raised (answers)

- Splash GitHub links 404 risk: repo IS public; links curl-checked at
  build time. Only the lit-lineage card was a dead link pre-push and is
  deliberately non-linked until this PR merges.
- `splash.ts:73` one-shot status fetch: accepted — single-shot landing
  page; badge staleness on an open tab is fine.

## Phase-3 open questions — Fable's answers (Sol pending)

1. **Pruning**: SPC-percentile as the slider, plus a per-node floor
   (keep each visible node's strongest in- and out-arc) as a
   connectivity guard. Do NOT export raw SPC ints (>2^53, JSON.parse
   loses precision) — export log10 or percentile rank.
2. **Roots vs backfill**: not alternatives — `cited_by` backfill adds
   *incoming* edges; it can't manufacture the roots' missing *outgoing*
   refs. Build the asserted-root curation layer now; backfill is a
   later nice-to-have for pre-1990 density.
3. **Dataset out of git**: OK, but "regenerable" is modulo upstream
   drift — archive the exact dataset the video is cut from as a
   release/deploy artifact, and add `retrieved_at` to the JSON `meta`.
4. **"Index is biased" framing**: lean in, but keep three mechanisms
   distinct on camera — coverage bias (Reuschel absent), metric
   behavior (SPC rewards Kerner's dense self-citation), and our own
   seed-query bias (assignment under-weighted).
