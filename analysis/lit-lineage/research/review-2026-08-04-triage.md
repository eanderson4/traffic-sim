# Review triage — PR #2 round (2026-08-04)

Round: Fable + Sol, same diff and brief. Archive:
`docs/kb/raw/reviews/2026-08-04T141629-47ce20a9-manual-*`
(run manually — `external-review.sh` needed GNU `timeout`, installed on
the machine mid-round; codex was authenticated later the same day).

## Blocker (Sol) — FIXED in this PR

`splash.ts` — the featured CTA POSTed to the admin-gated start endpoint
(ADR-0020), so public visitors hit a 401 whenever LA wasn't the active
run. Fixed: the LA entry is now resolved from `/api/demos` (hardcoded
fallback only for static previews); a 401/403 flips the card to a
watch-only posture (neutral info notice, CTA routes to `/demos.html`);
a registry without `la` disables the card the same way. `pnpm
check/build/test` green.

## Should-fix (deferred per AGENTS.md triage bar; fix before phase 3 binds outputs)

Flagged by BOTH reviewers:
1. `analyze.py` hub selection is nondeterministic at the top-25 cutoff
   (set-iteration order breaks ties). Fix: tie-break by work id.

Fable:
2. `harvest.py:127` — select-field rejection detection is a substring
   match on the error body; an unrelated 400 would silently drop `id`
   from `select=` and collapse all works into one `id: None` record.
   Match quoted field names instead.
3. `harvest.py:284` — re-running the harvest overwrites
   `research/first-pull.md`, which now has hand-written sections. Write
   the generated summary elsewhere or refuse when the hand-written
   marker is present.

Sol:
4. `harvest.py:305` — a failed seed query is logged and silently
   omitted; downstream outputs then overwrite good ones with a partial
   corpus. Fail the harvest or mark output partial.
5. `filter.py:133` — dedup drops duplicate work IDs without redirecting
   citations to the canonical survivor; references to discarded IDs
   stop resolving, shifting in-degree/SPC/main path.
6. `harvest.py:318` — `works.jsonl` emitted in API order; sort by
   stable id (downstream tie behavior depends on input order).
7. Register/distill the lit-lineage domain knowledge into `docs/kb/`
   (AGENTS.md rule 4 — currently only under `analysis/`).

## Nits (ignored unless touched anyway)

- `analyze.py:188` — key-route closing comment is backwards, code
  correct (both reviewers).
- `filter.py:151` — normalized-title dedup may merge distinct works;
  unaudited, note in phase-2.md (Fable).
- Pipeline order: `filter.py` after `anchors.py` silently drops anchor
  flags; add a guard (Fable).
- `analyze.py:242` vs `filter.py:171` — null-year sorts as 0 vs 9999
  (Fable).

## Questions raised (answers)

- Splash GitHub links 404 risk (Fable): repo IS public; links
  curl-checked at build time. Only the lit-lineage card was a dead link
  pre-push and is deliberately non-linked until this PR merges.
- `splash.ts` one-shot status fetch (Fable): accepted — single-shot
  landing page; badge staleness on an open tab is fine.

## Phase-3 open questions — reviewer answers

1. **Pruning** — agreement: SPC as the defensible spine, plus a
   per-node guard so dense schools can't dominate / thin branches
   vanish. Fable: export log10 or percentile rank, NOT raw SPC ints
   (>2^53 breaks JSON.parse); per-node floor = strongest in/out arc.
   Sol: cap per-node degree.
2. **Roots vs backfill** — DISAGREEMENT, needs a decision. Fable: not
   alternatives — `cited_by` backfill only adds *incoming* edges and
   can't manufacture the roots' missing *outgoing* refs, so the
   asserted-root curation layer is required now; backfill later for
   pre-1990 density. Sol: backfill first and record results; asserted
   edges only for remaining gaps, with explicit `asserted` provenance
   and receipts, never as ordinary citation edges. (Both agree on the
   provenance/receipts mechanism either way.)
3. **Dataset out of git** — agreement: acceptable only with durable
   provenance. Fable: archive the exact dataset the video is cut from
   as a release artifact + add `retrieved_at` to JSON meta. Sol: builds
   fetch a versioned, checksum-pinned artifact; "regenerate from
   current OpenAlex" is not reproducible enough.
4. **Bias framing** — agreement: lean in, stated narrowly. Fable: keep
   three mechanisms distinct (coverage bias / SPC metric behavior /
   our seed-query bias). Sol: the graph reflects OpenAlex coverage,
   metadata, and our choices — not an objective ranking; absence ≠
   proof of neglect.
