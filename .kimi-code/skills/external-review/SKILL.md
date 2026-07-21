---
name: external-review
description: Multi-model external review of a milestone or ADR — Claude Fable, GPT-5.6-sol, Gemini review the diff/design, findings triaged and fixed
type: prompt
whenToUse: After landing an ADR-implementing milestone, before any durable binding (recordings, content hashes, contract changes), or when the user asks for external/second-opinion review
---

# External Review

Two tiers (see AGENTS.md "Review Workflow"):

- **Per-commit gate** — mechanical: `scripts/external-review.sh` runs Claude
  Fable + GPT-5.6-sol over the staged diff and stamps it; the pre-commit
  hook enforces. You don't need this skill for that — just run the script
  before any code commit and triage what it prints.
- **Milestone round** (this skill) — the deeper pass below: all three
  models, a hand-written brief, ADR-level triage, archived round,
  hardening commit. Run it after ADR-implementing milestones, before
  durable bindings.

Get independent design+code review from the three external model CLIs, triage their findings against the actual code, fix what's real, and archive the round in the KB.

## Scope

$ARGUMENTS — typically a commit range (e.g. `HEAD~2..HEAD`) or a subsystem. If empty, use the commits since the last milestone.

## Instructions

### 1. Write the review brief

Write a brief to `/tmp/review-brief.md`. It must state:

- The commits/scope under review and the repo's one-paragraph context (deterministic Go engine, NATS contract, ADR-driven).
- The files to read first: the ADR(s), the implementation, the tests, and `git show <range>`; plus AGENTS.md and the ADRs that constrain the area (ADR-0005 determinism, ADR-0006 contract planes).
- Review focus, in priority order: correctness bugs; determinism risks (ADR-0005 discipline — map iteration, non-associative float sums, wall clock); design weaknesses relative to the ADR's own goals; contract/consistency issues (message contracts are sacred — additive fields still get documented in asyncapi); anything under-specified that will bite at the NEXT milestone.
- Output format: findings cited file:line, ranked blocker / should-fix / nit / question. Review only — DO NOT modify files.

Keep the brief self-contained: the reviewers start with zero context.

### 2. Launch all three reviewers in parallel (background)

```bash
# Claude Fable — NOTE: -p wants the brief on STDIN, not as an argument
claude -p --model fable --allowedTools "Read,Grep,Glob,Bash(git show:*),Bash(git log:*)" \
  < /tmp/review-brief.md > /tmp/review-fable.md 2>/tmp/review-fable.err

# GPT-5.6-sol (codex) — read-only sandbox; Go tests can't run in it, that's fine
codex exec -m gpt-5.6-sol --sandbox read-only "$(cat /tmp/review-brief.md)" \
  > /tmp/review-sol.md 2>/tmp/review-sol.err

# Gemini
gemini -p "$(cat /tmp/review-brief.md)" > /tmp/review-gemini.md 2>/tmp/review-gemini.err
```

Run them as background tasks. While they run, do NOT start dependent work that the review might reshape.

### 3. Triage — verify before fixing

For every finding, confirm it against the actual code/behavior before acting:

- Reproduce claims with a small probe when cheap (precedent: the "yaml.v3 silently truncates 1.9→1 into int fields" claim was verified with a 20-line program before the fence was rewritten).
- Classify: **fix now** (correctness, determinism, contract) / **document** (defensible-but-surprising semantics → ADR addendum or code comment) / **defer** (next milestone's scope — record it in the ADR's open list) / **reject** (wrong premise — say why in the commit or addendum).
- Findings that contradict each other get a decision, not a compromise.
- **Triage bar (AGENTS.md "Review Workflow"): blockers only, one round.** Should-fixes get recorded and deferred, nits ignored, and hardening against hypotheticals (error handling for cases that can't yet occur) is reject-by-default at this stage. Do not run fix-and-re-review loops for polish — one round per commit.

### 4. Fix and verify

- Apply fixes in dependency order (package internals → wiring → docs).
- Add a regression test for every fixed bug; run the full suite plus the milestone's acceptance gate (e.g. pinned CRCs).
- Update the ADR addendum and asyncapi/KB when the contract or format semantics moved.

### 5. Archive the round

- Copy brief + reviews to `docs/kb/raw/reviews/YYYY-MM-DD-<scope>-{brief,claude-fable,gpt-5.6-sol,gemini}.md`.
- Add a review-provenance note to the relevant ADR addendum (what was caught, what was deferred).
- Add a KB freshness note to `docs/kb/articles/gaps-and-roadmap.md`.
- Commit as its own commit ("harden after external review"), separate from the milestone commit.

## Important

- The value is in the triage, not the volume: Gemini alone found 1 should-fix; Fable and Sol each found design-level bugs the others missed. Run all three.
- External reviewers can't be trusted on facts — every file:line claim gets checked before it costs an edit.
- Nothing durable (recordings, hash bindings, published contracts) should ship between the milestone commit and the review round — the round is cheapest when identity definitions can still move.
