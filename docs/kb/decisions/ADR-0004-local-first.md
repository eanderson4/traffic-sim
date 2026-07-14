# ADR-0004: Local-first deployment via docker-compose

- **Status:** Accepted
- **Date:** 2026-07-14

## Context

The first deliverable is a demo for a math-vs-vibes episode running on one machine.
Cloud hosting adds setup and operational cost with no near-term payoff. The
multiplayer chaos demo can run over LAN/Tailscale.

## Decision

Everything runs locally: NATS via docker-compose, engine and controllers as local
processes, visualization in the browser. No cloud infrastructure for now.

## Consequences

- Zero hosting cost/ops during development and for the episode.
- Design constraint: nothing may *preclude* hosted deployment later — no localhost
  assumptions baked into message contracts, config, or auth-shaped decisions.
- When public multiplayer matters, revisit with a new ADR (VPS/fly.io + NATS
  auth/accounts).
