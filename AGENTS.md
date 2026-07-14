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

## Conventions

- Go: standard library first; justify dependencies. `gofmt` + `go vet` clean.
- TS: no framework by default; pnpm; strict mode.
- Tests accompany behavior, especially anything affecting determinism/replay.
- Commit messages: imperative mood, reference ADRs when implementing one.
