---
name: distill-kb
description: Distill raw KB research into polished articles, cross-topic concerns, and high-level summaries
type: prompt
whenToUse: When the user asks to distill KB research into articles/summaries after topics have been researched
---

# Distill Knowledge Base

Read all raw research in `docs/kb/raw/` and produce distilled knowledge: polished articles, cross-topic analysis, high-level summaries, and gap identification.

## Target

$ARGUMENTS

If no argument provided, default to `--all`.

## Instructions

### 1. Load Raw Research

Read every file in `docs/kb/raw/`. For each:
- Note the topic title, category, and status
- Note the "Connections to Other Topics" sections
- Build a mental map of how topics relate

Read `docs/kb/.kb-meta.json` for metadata.
Read `docs/kb/INDEX.md` for the topic registry.

If no raw research exists, tell the user to run `/skill:research-topic --all` first.

### 2. Cross-Topic Analysis (`--cross-topics` or `--all`)

Analyze all raw research together to find patterns that span topics:

- **Architectural invariants**: Rules that hold across the entire codebase (e.g., "all state flows through Zustand stores", "every manager is a singleton")
- **Shared conventions**: Naming, error handling, testing patterns used consistently
- **Recurring gotchas**: Similar pitfalls across different subsystems
- **Dependency chains**: Which topics must be understood before others
- **Contradictions**: Places where different subsystems handle things inconsistently

Write to `docs/kb/articles/cross-topic-concerns.md`:

```markdown
# Cross-Topic Concerns

> Patterns, invariants, and conventions that span multiple areas of the codebase.

## Architectural Invariants

- [Invariant] — spans [topic A], [topic B]. [Why it matters.]
- ...

## Shared Conventions

- [Convention] — observed in [topic C], [topic D], [topic E]. [Brief explanation.]
- ...

## Recurring Gotchas

- [Gotcha] — affects [topics]. [What to watch out for.]
- ...

## Reading Order

For newcomers, understand topics in this order:
1. [Glossary / Domain Concepts] — vocabulary first
2. [System Overview] — big picture
3. [Most foundational pattern] — the core abstraction
4. Then: whatever is relevant to your task

## Inconsistencies

- [Where subsystem A does X but subsystem B does Y] — [possible reason]
- ...

---
*Derived from: [list of raw research files]*
```

### 3. Distill Articles (`--articles` or `--all`)

For each completed raw research topic, produce a polished article in `docs/kb/articles/{category}/`.

Create category directories as needed:
```bash
mkdir -p docs/kb/articles/{architecture,concepts,workflows,patterns,decisions,integrations}
```

**Article template:**

```markdown
# [Title]

> [One-sentence summary — this appears in INDEX.md for scanning]

## Overview

[2-3 paragraphs explaining the topic. Written for someone technically
competent but new to this codebase. Focus on understanding, not exhaustive
detail. The raw research file has the full details.]

## Key Components

| Component | Location | Purpose |
|-----------|----------|---------|
| `ClassName` | `path/to/file.ts` | What it does in one line |
| `FunctionName` | `path/to/other.ts` | What it does in one line |

## How It Works

[Explain the flow or structure. Use numbered lists for sequential processes,
bullet lists for parallel concerns. Point to code locations rather than
copying source code.]

1. [First step] (`path/file.ts:N`)
2. [Second step] (`path/other.ts:M`)
3. ...

## Gotchas

- **[Gotcha title]**: [Explanation of non-obvious behavior and why it exists]
- **[Gotcha title]**: [Another one]

## Related

- [Other Article Title](../category/article.md) — [one line on how it connects]
- [Another Article](../category/other.md) — [relationship]

---
*Raw research: [raw/topic-name.md](../raw/topic-name.md)*
```

**Distillation guidelines:**
- Articles are for LLM consumption — scannable, concise, progressive
- The raw research file is always linked for traceability
- Don't duplicate content from existing docs (README, AGENTS.md) — link to them
- Prefer pointing to code locations over copying code
- Each article should be self-contained enough to understand without reading others, but link to related articles for deeper context

### 4. High-Level Summary (`--summary` or `--all`)

Generate `docs/kb/articles/summary.md` — the "executive briefing" for someone new:

```markdown
# [Project Name] — Knowledge Base Summary

> [What this project is, in one paragraph]

## Architecture at a Glance

[One paragraph overview. What are the major pieces and how do they fit together?]

| Subsystem | Purpose | Key File(s) |
|-----------|---------|-------------|
| ... | ... | ... |

## Key Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| [Why this framework?] | [Choice] | [Brief reason] |
| [Why this pattern?] | [Choice] | [Brief reason] |

## Current State

- **Strengths:** [What's well-built and stable]
- **In flux:** [Areas actively changing or being refactored]
- **Known gaps:** [Technical debt or missing functionality]

## Reading Paths

**New contributor:**
1. [Summary](summary.md) (you are here)
2. [Glossary](concepts/domain-glossary.md)
3. [System Overview](architecture/system-overview.md)
4. [Development Workflow](workflows/development-setup.md)

**Feature developer:**
1. [System Overview](architecture/system-overview.md)
2. [Relevant pattern article]
3. [Relevant concept article]

**DevOps / Deployment:**
1. [Deployment Pipeline](workflows/deployment-pipeline.md)
2. [Architecture Overview](architecture/system-overview.md)

---
*Distilled from [N] raw research files on [date]*
```

### 5. Gaps & Roadmap (`--all`)

Generate `docs/kb/articles/gaps-and-roadmap.md`:

```markdown
# Knowledge Base Gaps & Roadmap

> Areas that need more research, plus suggested improvements.

## Under-Documented Areas

| Area | Why It Matters | Suggested Topic |
|------|---------------|-----------------|
| [Code area] | [Impact] | [Proposed topic name] |

## Open Questions (Aggregated)

Collected from all raw research files:
- [Question from topic A] — *from [raw/topic-a.md]*
- [Question from topic B] — *from [raw/topic-b.md]*

## Suggested Next Research

1. [Topic idea] — [why it would be valuable]
2. [Topic idea] — [why]

## Freshness Notes

- [N] topics researched as of [date]
- Oldest research: [topic] ([date])
- Run `/skill:update-kb` to check for stale topics

---
*Generated: [date]*
```

### 6. Update INDEX.md

Transform INDEX.md from a topic registry into a navigation hub. Keep the checklist for tracking, but add article links:

```markdown
# Knowledge Base: [Project Name]

> [One-line description]

## Start Here

- [Summary](articles/summary.md) — project overview and reading paths
- [Domain Glossary](articles/concepts/domain-glossary.md) — terminology and definitions
- [Cross-Topic Concerns](articles/cross-topic-concerns.md) — patterns spanning the codebase

## Articles

### Architecture
- [Article Title](articles/architecture/name.md) — one-line summary

### Concepts
- [Article Title](articles/concepts/name.md) — one-line summary

### Workflows
- [Article Title](articles/workflows/name.md) — one-line summary

### Patterns
- [Article Title](articles/patterns/name.md) — one-line summary

### Decisions
- [Article Title](articles/decisions/name.md) — one-line summary

## Raw Research

All source-attributed research files: [raw/](raw/)

## Gaps & Roadmap

- [Gaps & Roadmap](articles/gaps-and-roadmap.md)

## Topic Registry

### Architecture
- [x] [Topic](raw/topic.md) — description
...

---
*Last distilled: [date] | [N] articles from [M] raw research files*
*Run `/skill:update-kb` to check freshness*
```

### 7. Update Metadata

Update `.kb-meta.json` articles section:
```json
{
  "articles": {
    "articles/architecture/system-overview.md": {
      "distilled_at": "[ISO date]",
      "derived_from": ["raw/architecture-topic.md", "raw/concepts-topic.md"],
      "summary": "One-line summary"
    }
  },
  "last_distilled": "[ISO date]"
}
```

## Important

- Distillation reads raw research — it does NOT re-scan the codebase
- Every article links back to its raw research for traceability
- Don't duplicate existing docs — link to AGENTS.md, README, etc.
- Articles should be scannable: headers, tables, short paragraphs
- Cross-topic analysis is the most valuable output — find the patterns humans miss
- If raw research is incomplete (has many open questions), note this in the article rather than guessing
