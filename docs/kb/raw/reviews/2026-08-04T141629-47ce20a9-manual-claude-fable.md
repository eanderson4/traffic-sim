## Review — PR #2 (lit-lineage pipeline + splash page)

No blockers. The demosrv Go change is a clean, complete rename (no dangling `handleMenu` refs, no import changes, `/demos.html` genuinely falls through to the `FileServer` at main.go:427); the route table is otherwise untouched, so existing routes and the map app are safe even unbuilt. The splash wiring matches `deploy/demos.public.json` (`id: "la"`, `run: "la"`) and demos-core's contracts. Findings:

### Should-fix

1. **`harvest.py:127` — select-field rejection detection is a substring match on the error body.** `dropped = [f for f in ... if f and f in body]`: the field `"id"` is a substring of `"invalid"`, which appears in most OpenAlex 400 bodies. Any unrelated 400 (bad cursor, malformed query) would silently drop `id` from the select and retry; works then normalize with `id: None` and `works_by_id.setdefault(None, ...)` (harvest.py:313) collapses every result into one record. Latent today (no 4xx triggered per first-pull.md), but the failure mode is silent corpus corruption. Match quoted field names or the API's structured error field.

2. **`harvest.py:284` — re-running the harvest destroys hand-written research content.** `write_summary` unconditionally overwrites `research/first-pull.md`, but the checked-in file now contains hand-authored sections ("Filter notes for phase 2", the entire "Known gaps and API quirks" section) that aren't in the template. The README explicitly invites re-running harvest.py with cached pages, which would clobber them. Recoverable via git, but a footgun: write the generated summary to a separate file, or refuse to overwrite when the `<!-- Hand-written filter notes -->` placeholder is absent.

3. **`analyze.py:114–118, 300` — hub tags are not run-to-run deterministic.** `in_deg` is populated by iterating the `edges` *set* in `build_graph`, so dict insertion order depends on Python's randomized string hashing; `sorted(in_deg.items(), key=lambda kv: -kv[1])[:HUB_TOP_N]` then breaks ties at the top-25 boundary by that order. If two works tie in in-degree at rank 25, the `hub` tag set differs across runs. One-char fix: `key=lambda kv: (-kv[1], kv[0])`. (Everything else I traced — topo order, SPC, key-route tie-breaks, co-authorship — is properly sorted; SPC on arbitrary-precision ints avoids float accumulation entirely. This is the one leak.)

### Nits

4. **`analyze.py:188` — the key_route closing comment is backwards.** "head was built oldest->newest (a is the older end)": edges run citing→cited (new→old), so `a` is the *newer* end and the head walk along `in_adj` moves toward newer works. The `head[::-1] + tail` reversal is nonetheless correct (verified: result is newest-first, consecutive pairs are real arcs); only the justification misleads the next reader.

5. **`viz/src/splash.ts:10–18` — the LA `DemoInfo` is hardcoded even though `/api/demos` is already fetched.** If the registry id/run for LA ever changes, the card 404s on start with no compile-time or runtime signal. Resolving the entry by id from the `reg` payload (falling back to the hardcode for static previews) would drop the duplication.

6. **`filter.py:151` — normalized-title dedup can merge distinct works.** Short generic titles ("Traffic dynamics") or preprint/journal pairs collapse to one record; 186 drops is large enough to contain a few false merges. Probably desirable on net, but unaudited — worth a one-line note in phase-2.md next to the DOI-dedup audit.

7. **`anchors.py` / pipeline order — a stale-order run silently loses anchors.** `filter.py` rewrites `corpus.jsonl` from `works.jsonl`, wiping `anchor: true` flags; nothing detects it, and `analyze.py` runs happily on the anchor-less corpus. A cheap guard (anchors.py stamping a marker filter.py doesn't emit, analyze warning when zero anchors present) would catch it.

8. **`analyze.py:242` vs `filter.py:171` — inconsistent null-year handling**: analyze's `order_key` treats missing year as 0 (oldest), filter's sort as 9999 (newest). Harmless at current corpus size, but pick one.

### Questions

9. **`viz/splash.html:142–179`** — the analysis cards and footer hard-link to `github.com/eanderson4/traffic-sim/blob/main/docs/...`. Is the repo public and are those `docs/show` paths already on `main`? The lit-lineage card correctly avoids a dead link; the other five links and the footer will all 404 if the repo is private or the files move.

10. **`viz/src/splash.ts:73`** — status is fetched once at page load; the "running" badge goes stale if a run starts/stops while the page is open. Deliberate (single-shot landing page) or an oversight?

---

## Phase-3 thoughts

**1. SPC-weight vs top-k.** SPC as the primary slider, but with two amendments. First: don't export raw SPC values — they're arbitrary-precision ints that can exceed 2^53, and `JSON.parse` in the browser will silently lose precision or misbehave; export log10 or percentile rank instead. Second: a global SPC threshold will amputate side-branches wholesale (the assignment branch, thin already, vanishes first) because path counts concentrate combinatorially along the main corridor. Use SPC-percentile for the slider plus a per-node floor (keep each visible node's single strongest in- and out-arc) as a connectivity guard — that's top-k with k=1 serving as SPC's safety net, not a competing scheme.

**2. Asserted roots vs backfill first.** These aren't alternatives. The `cited_by` backfill adds *citing* works — it thickens edges into the hubs and improves resolution percentage, but cannot manufacture the roots' missing *outgoing* references, which is the actual reason the computed path starts at 1958. So the asserted-root curation layer is required regardless; build it now (the receipts-in-`note` mechanism is right, and keeping hand edits in a tracked file applied as the last pipeline step is exactly the right separation). Backfill is an orthogonal, later, nice-to-have for pre-1990 tree density.

**3. Pruned JSON out of git.** No objection to the mechanism, one caveat: "regenerable" is only true modulo upstream drift — OpenAlex is live, and a re-harvest next year returns different citation counts and possibly different records, while the raw-page cache that pins reproducibility is also gitignored. Per your own external-review rule ("before anything durable binds the result — recordings, content hashes"), once the video ships you'll want the exact dataset it was cut from. Keep it out of git, but archive the shipped dataset alongside the deploy/video artifact (or a release asset), and add a `retrieved_at` date to the JSON `meta` — it currently has provenance but no timestamp.

**4. "The index is biased" framing.** Lean in — it's the strongest act you have, and the evidence is already collected. But keep two distinct mechanisms distinct on camera, or a pedantic viewer will conflate them and dismiss both: Reuschel's absence is *coverage* bias (what the index sees — English-language, OR-journal priority), while Kerner's dominance is *metric* behavior (SPC rewards a densely self-citing school; that's a property of main-path analysis on any citation graph, not of OpenAlex). And complete the honesty move by including the third bias — your own seed queries (phase-2 admits assignment is under-weighted). "The index is biased, the metric is biased, and so is my harvest — here's how you reason anyway" is a stronger math-vs-vibes thesis than pointing only at the index.

REVIEW-COMPLETE
