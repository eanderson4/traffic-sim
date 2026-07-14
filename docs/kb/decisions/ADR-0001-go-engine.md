# ADR-0001: Go for the engine core, TypeScript for visualization

- **Status:** Accepted
- **Date:** 2026-07-14

## Context

The sim engine needs solid concurrency, good NATS integration, and enough performance
headroom to eventually replay days of city traffic. Visualization and human-driver
clients live in the browser. The project will be open-sourced, and alignment with the
NATS ecosystem (NATS is written in Go) is both a technical and community/credibility
story. Rust was considered (max performance) but iterates slower; TypeScript-everywhere
was considered (one toolchain) but gives up engine headroom.

## Decision

Engine core and other backend services: **Go**. Visualization and web/browser
clients: **TypeScript**. Controllers may be any language with a NATS client — the
message contract is the interface, not a language SDK.

## Consequences

- Two toolchains; message schemas must be defined language-neutrally (schema files +
  codegen or careful duplication — settle in `arch-nats-backbone` research).
- Option to embed nats-server in the engine binary for zero-dependency demos.
- Per ADR-0002 and the microservice-shaped boundary principle (VISION §Architecture
  Principles), the engine can later be rewritten (e.g. Rust) without touching
  controllers or viz.
