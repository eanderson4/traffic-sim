# AGENTS.md — Rules of Engagement

Rules for anyone (human or agent) working in this repo.

## Ground Rules

1. **Read `docs/VISION.md` first.** It is the founding document. If a task seems to
   contradict it, raise the conflict instead of silently diverging.
2. **Consult the KB before designing.** `docs/kb/INDEX.md` is the topic registry.
   If a KB article or decision record covers your area, follow it. If your work
   contradicts the KB, the KB is stale — fix it as part of your change.
3. **Decisions of consequence get an ADR** in `docs/kb/decisions/` (sequential
   `ADR-NNNN-slug.md`). "Of consequence" = affects message contracts, service
   boundaries, data models, dependencies, or anything expensive to reverse.
4. **Keep the KB current.** New domain knowledge learned while building (traffic
   engineering facts, NATS behaviors, OSM quirks) belongs in the KB, not just in
   code comments or PR descriptions.
5. **Message contracts are sacred.** Anything published on NATS subjects is a public
   interface between services. Changing a subject name or payload schema requires an
   ADR and a migration note.

## Architecture Invariants (see ADRs for rationale)

- Engine core: **Go**. Visualization/web clients: **TypeScript**. (ADR-0001)
- All inter-service communication flows over **NATS** (core / JetStream / KV). No
  direct service-to-service RPC. (ADR-0002)
- Visualization is **MapLibre-first**, vanilla TS, no UI frameworks without an ADR.
  (ADR-0003)
- Local-first: everything runs via docker-compose + local processes. (ADR-0004)
- The engine is authoritative over world state; controllers only emit intents.

## KB Workflow

- `/research-topic <name>` — deep research on one registered topic → `docs/kb/raw/`
- `/distill-kb` — synthesize raw research into `docs/kb/articles/`
- `/update-kb` — freshness check; run when returning after a gap

## Review Workflow

- **Every code commit is externally reviewed.** A pre-commit hook
  (`hooks/`, enabled via `git config core.hooksPath hooks` — set once per
  clone) gates EVERY commit except documentation and generated content
  (`docs/`, `data/`, `viz` dist/node_modules/public, root
  Markdown, LICENSE, root `.gitignore`/`.gitattributes` — subdirectory
  ones are gated). Anything else — engine,
  viz sources, contracts, CI, analysis, prototypes, or the gate mechanism
  itself — requires the exact staged diff to have been reviewed by Claude
  Fable and GPT-5.6-sol. `scripts/external-review.sh` runs both over the
  staged diff, archives the round to `docs/kb/raw/reviews/`, and stamps
  the REVIEWED snapshot; staging anything afterwards fails the gate, so
  review always covers what ships. The gate proves review *happened* —
  triaging findings is the committer's job. Docs/KB-only commits pass
  ungated. Escape hatch: `EXTERNAL_REVIEW_SKIP=1` (noisy on purpose).
  When Fable is unavailable (quota, outage), `--kimi` substitutes Kimi K3
  in its slot — still two model families, and the archive names whoever
  actually reviewed (ADR-0013 addendum 2026-07-27).
  Archive policy: commit the reviews themselves (brief, diffstat, model
  outputs); the `*-reviewed.patch` snapshot is written locally but
  gitignored — it is byte-reproducible from git and was nearly all of the
  archive's weight (ADR-0013 addendum 2026-08-04).
- **Triage bar: blockers only, one round.** Fix blockers before
  committing; record should-fixes in the commit message or KB and defer
  them; ignore nits. Reviewers will always find one more edge case — at
  this stage "too early to need this" is a valid triage outcome, and
  hardening against hypotheticals is a reject-by-default. One review
  round per commit, not a fix-and-re-review loop for polish.
- `/external-review <scope>` — the deeper milestone round (all three
  models incl. Gemini, richer brief, ADR-level design questions). Run it
  after every ADR-implementing milestone, before anything durable binds
  the result (recordings, content hashes, published contract consumers).
  First run: M11 (2026-07-21) — caught the seed-in-hash identity bug and
  unexecuted demand parts before either could ship.

## Conventions

- Go: standard library first; justify dependencies. `gofmt` + `go vet` clean.
  Approved exception (ADR-0006): the two official NATS modules —
  `github.com/nats-io/nats.go` and `github.com/nats-io/nats-server/v2`
  (embedded tests) — confined to `engine/natsio/`; the kernel package stays
  stdlib-only. Justification lives in the `natsio` package doc.
  Second approved exception (ADR-0012 §2): `gopkg.in/yaml.v3`, confined to
  `engine/scenario/`; justification lives in the `scenario` package doc.
- TS: no framework by default; pnpm; strict mode.
- Tests accompany behavior, especially anything affecting determinism/replay.
- Commit messages: imperative mood, reference ADRs when implementing one.
