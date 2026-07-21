# ADR-0013: External multi-model review as a commit gate

- **Status:** ACCEPTED (ratifies the M11 review round and the gate tooling
  that grew out of it)
- **Date:** 2026-07-21

## Context

M11 (scenario directories) was reviewed after implementation by three
external models (Claude Fable, GPT-5.6-sol, Gemini — archived in
`docs/kb/raw/reviews/`). The round caught two design-level defects before
anything durable bound them — the seed-in-hash run-identity incoherence
and validated-but-never-executed scenario demand — plus a dozen
correctness bugs none of the authors' own tests had. The repo's own
evidence is that external review catches what self-review misses,
particularly around identity semantics and "tested but not wired" gaps.

Making proprietary external CLIs a *mandatory* step for every code commit
is itself a decision of consequence (AGENTS.md rule 3): it adds a cost, a
network dependency, and a trust boundary. This ADR records it instead of
letting it accrete in tooling.

## Decision

1. **Every commit touching code, contracts, or tooling is externally
   reviewed before it lands.** A pre-commit hook (`hooks/pre-commit`,
   enabled per clone via `git config core.hooksPath hooks` — note this
   replaces `.git/hooks` wholesale in that clone) gates EVERY commit
   except documentation and generated content (`docs/`, `data/`,
   `viz` dist/node_modules/public, root Markdown, LICENSE, root
   .gitignore/.gitattributes — subdirectory ones are gated). The listing
   is inverted — everything not
   explicitly documentation is gated — so future tooling directories
   cannot slip past by omission. A `pre-merge-commit` hook applies the
   same gate to auto-merges; rebase/cherry-pick create commits outside
   both hooks and are an accepted gap (their content was reviewed on its
   original branch). A message-only `git commit --amend` likewise fails
   the gate (the base HEAD moved); re-run the review or use the hatch.
   `scripts/external-review.sh` produces the stamp: it runs **Claude
   Fable** and **GPT-5.6-sol** over the staged diff, requires both to
   complete, archives the reviews *and the reviewed patch* to
   `docs/kb/raw/reviews/`, and stamps the tree hash captured before the
   reviewers ran (staging anything afterwards fails the gate — review
   always covers what ships). Docs/KB-only commits are ungated.
2. **Two required reviewers, not consensus.** Two independent model
   families (Anthropic, OpenAI) are the gate; Gemini joins milestone
   rounds (`--gemini`) where the design surface is larger. The M11 round
   showed the value is diversity — each model caught bugs the others
   missed — not unanimity.
3. **The gate proves review *happened*, not that it was *clean*.** The
   stamp does not encode findings. Triage — verify every claim against
   the code, fix with regression tests, document defensible-but-surprising
   semantics in ADR addenda — remains the committer's job, per the
   `/external-review` skill. This is the deliberate trust boundary: the
   hook enforces process, judgment stays human/agent-side.
4. **Failure policy is fail-closed with a loud escape hatch.** A reviewer
   that fails, empties out, or misses its exact-line completion marker =
   no stamp = no commit. CLI/network unavailability does not silently
   waive review; `EXTERNAL_REVIEW_SKIP=1` bypasses, prints a warning, and
   is expected to be rare and self-justifying in the commit message.
   Partial commits (`git commit -- <paths>`, `-p`, `-a`) use a temporary
   index and pass only if that index matches the stamped tree exactly —
   in practice: stage explicitly. Merge resolutions touching gated paths go
   through the SAME gate — conflict resolutions and anything staged while
   `MERGE_HEAD` exists are novel, unreviewed content; there is no merge
   waiver. The stamp binds (base HEAD, index tree), so a `reset --soft`
   cannot rebase a review onto different content. The stamp is trivially
   forgeable by hand, and reviewed content can address the reviewers
   directly (prompt injection rides inside the diff) — both accepted:
   the gate is a process guard for honest actors, not a security
   boundary, and hand-forging is strictly more effort than the documented
   skip hatch. Reviewer isolation is asymmetric by necessity (Fable gets a
   tool allowlist, Sol a read-only sandbox, Gemini none — recorded, not
   solved); git's standard `--no-verify` bypasses any client-side hook by
   design. All of these live inside the honest-actor boundary: this gate
   is not a security control and is not presented as one.
5. **The mechanism reviews itself.** `hooks/` and `scripts/` are inside
   the gated path set, so changes to the gate go through the gate. The
   bootstrap commit was itself dogfooded through the gate's own review
   rounds before landing; on a fresh repo (unborn HEAD) the very first
   commit still needs the skip hatch.
6. **Milestone rounds are the deeper tier.** After ADR-implementing
   milestones — before durable bindings (recordings, content hashes,
   published contract consumers) — the `/external-review` skill runs all
   three models with a hand-written brief, and the round gets provenance
   in the ADR addendum plus a KB freshness note.

## Consequences

- Every code commit costs two external review calls (minutes of wall
  time, API quota). Accepted: the M11 round's catch rate justifies it,
  and the reviews double as an audit archive in the KB.
- Smaller accepted limitations (triage bar: deferred, not fixed):
  reviewers read the live working tree for context, so unrelated
  unstaged edits can color a review — the archived reviewed patch keeps
  drift detectable; the hook's NUL→newline path conversion can
  mis-classify a newline-bearing filename as docs-only (no such files
  exist here); AGENTS.md, the normative policy text, is itself ungated
  root Markdown and can drift from the gated mechanism. All sit inside
  the honest-actor boundary.
- The workflow depends on three proprietary CLIs being installed and
  authenticated on the dev machine; a machine without them can only
  commit via the skip hatch, which is visible in the terminal log.
- Reviews are archived per commit in `docs/kb/raw/reviews/` (untracked at
  review time; committed separately, docs-only, ungated — the archive is a
  process record, not a cryptographic audit trail: each round includes
  the byte-exact reviewed patch, so tampering is detectable in principle,
  but nothing enforces it; accepted under the honest-actor boundary).
  Repo growth is bounded by review size; prune by policy later if it
  matters.
- Revisit when: review latency measurably slows iteration (batching
  policy), a reviewer CLI disappears (substitute a model family — keep
  two families), or the gate starts rubber-stamping (strengthen the
  completion marker into structured verdicts).
