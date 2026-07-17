---
name: research-topic
description: Deep research a single knowledge base topic — produces raw source-attributed findings
type: prompt
whenToUse: When the user asks to research a KB topic (or all pending topics) under docs/kb
---

# Research Topic

Run deep, independent research on one or more KB topics. Produces raw research files with full source attribution for later distillation.

## Target

$ARGUMENTS

## Instructions

### 1. Load KB Context

Read `docs/kb/INDEX.md` and `docs/kb/.kb-meta.json`.

- If KB doesn't exist, tell the user to run `/skill:init-kb` first.
- If `$ARGUMENTS` is empty, list available topics and ask which to research.
- If `$ARGUMENTS` is `--all`, find all topics with status `pending` or marked `[ ]` in INDEX.md.
- Otherwise, match `$ARGUMENTS` to a topic filename in the meta.

### 2. Determine Scope

For the target topic(s), read from `.kb-meta.json`:
- Topic title and category
- Key files to investigate
- Complexity estimate
- Any previous research state (for re-research of stale topics)

If re-researching a stale topic, read the existing raw file first to understand what changed.

### 3. Deep Research (Per Topic)

For each topic, launch 1-2 focused Explore agents. The number depends on complexity:
- **shallow**: 1 agent
- **medium/deep**: 2 agents with different focus areas

**Primary research agent prompt:**
```
"Deep-dive research on [TOPIC TITLE] in this codebase.

Starting points: [KEY FILES from meta]

Research approach:

1. PRACTICAL — Trace the actual code:
   - Read the key files thoroughly
   - Trace code paths end-to-end (callers → callees)
   - For each component: What does it do? Why does it exist? How does it connect?
   - What would trip up a newcomer?
   - What's the happy path? What are the edge cases?
   - Find actual usage examples in tests or consuming code

2. ACADEMIC — Identify patterns and context:
   - What design patterns are being used? (mediator, observer, registry, factory, etc.)
   - How does this compare to standard implementations of those patterns?
   - Are there deviations from the standard pattern? Why might they exist?
   - What are the known trade-offs of this approach?

For EVERY finding, include:
- Exact file path and line numbers (e.g., src/managers/SceneManager.ts:45-120)
- Quote or paraphrase the relevant code
- Your analysis of WHY, not just WHAT
- Connections to other parts of the codebase
- Anything non-obvious, surprising, or potentially confusing

Be thorough — this is raw research. More detail is better.
Include open questions where the code is ambiguous or intent is unclear."
```

**Secondary agent (for medium/deep topics):**
```
"Research external context for [TOPIC TITLE]:

1. Search the web for documentation, best practices, and known issues related to:
   - [Specific frameworks/libraries used by this topic]
   - [Design patterns identified]
   - [Domain-specific concepts]

2. Find:
   - Official documentation for key dependencies
   - Blog posts or articles about this pattern/approach
   - Known gotchas or performance considerations
   - Alternative approaches and why they weren't chosen (if inferable)

3. Academic context:
   - Name the design patterns formally
   - Reference relevant software architecture principles
   - Note any anti-patterns that should be avoided

Return findings with URLs/references for each source."
```

### 4. Write Raw Research Files

Create a folder `docs/kb/raw/{topic-name}/` with 4 files:

#### `implementation.md` — Code tracing with file:line attribution
```markdown
# Implementation: [Topic Title]

> Source: codebase tracing | Researched: [YYYY-MM-DD] | Git HEAD: [7-char hash]

## [Component 1]

[Detailed code tracing. Every claim needs file:line attribution.]

**Source files:**
- `path/to/file.ts:45-120` — [what this section contains]

**Analysis:** [Why this works this way, not just what it does.]

## [Component 2]
...
```

#### `competitors.md` — Named tool comparisons
```markdown
# Competitors: [Topic Title]

> Source: web research + codebase analysis | Researched: [YYYY-MM-DD]

## Competitive Landscape
[Where this tool sits in the market. What niche it fills.]

## [Competitor 1]
- What it does, key capabilities
- **vs this project:** [Specific comparison — what's better/worse/different]
- Source: [URL]

## [Competitor 2]
...

## Positioning Summary
[Table or narrative comparing all competitors on key dimensions]
```

#### `standards-and-patterns.md` — Standards, design patterns, academic references
```markdown
# Standards & Patterns: [Topic Title]

> Source: academic research + pattern identification | Researched: [YYYY-MM-DD]

## [Standard 1] (e.g., ISO 9613-2, IFC, GeoTIFF)
[What the standard defines. How our implementation relates to it.]
Source: [URL]

## Design Patterns Identified
### [Pattern Name]
[What pattern, where it's used, why it fits, trade-offs.]

## [Academic Concept]
[Relevant theory, algorithms, or industry best practices.]
```

#### `synthesis.md` — Aggregate connecting all sources
```markdown
# Synthesis: [Topic Title]

> Researched: [YYYY-MM-DD] | Git HEAD: [7-char hash] | Status: complete

## Summary
[2-3 sentences. Where it sits in the competitive landscape.]

## Source Files
- [Implementation trace](./implementation.md)
- [Competitor analysis](./competitors.md)
- [Standards & patterns](./standards-and-patterns.md)

## Key Architectural Decisions
### [Decision 1]
**Choice:** [What was chosen]
**Why:** [Rationale from code/docs]
**Trade-off:** [What was given up]
**Field context:** [How this compares to industry standard. Link to source file.]

## Compare/Contrast: Our Approach vs the Field
[Table comparing our implementation against competitors/standards on key dimensions]

## Open Questions
- [Unresolved from code alone — need author input]

## Connections to Other Topics
- **Relates to:** [links to other topic folders]
- **Depends on:** [prerequisite topics]
- **Informs:** [downstream topics]
```

**Key principles:**
- Field knowledge must be **integrated inline** with implementation analysis, not appended as a flat reference list
- Every competitor comparison must be **specific** (named features, not "more powerful")
- Standards must be **connected** to our implementation (how we comply, deviate, or simplify)
- synthesis.md ties the three source files together with cross-cutting compare/contrast

### 5. Update Metadata

After writing the raw files:

**Update `.kb-meta.json`:**
- Set topic status to `"complete"`
- Record source files with their git blob hashes:
  ```bash
  git rev-parse HEAD:path/to/file.ts
  ```
- Record the generation timestamp
- Add `raw_files` listing the 4 files in the topic folder

**Update `INDEX.md`:**
- Change `- [ ]` to `- [x]` for the researched topic
- Link should point to `raw/{topic-name}/synthesis.md`

### 6. Post-Research Audit

After writing raw files for a batch, run an **audit subagent** to verify factual accuracy. The audit agent should:

1. Read the KB files just written
2. Read the actual source code files cited
3. Spot-check 3-5 specific claims per topic (line numbers, function names, logic descriptions)
4. Check cross-file consistency (implementation.md vs synthesis.md)
5. Score each topic on 6 dimensions (1-5): source attribution, WHY not WHAT, field research quality, cross-file consistency, actionable for LLM, factual accuracy
6. Report issues with severity (critical/moderate/minor) and what the KB says vs what the code shows

**Fix all moderate+ issues before proceeding to the next batch.** This step is critical — research agents make factual errors (wrong line numbers, misattributed behavior, inverted values) that compound if unfixed.

### 7. Batch Mode (--all)

When researching all pending topics:

1. Read INDEX.md, collect all `[ ]` (unchecked) topics
2. Group by category. Research order: business-domains → concepts + architecture → patterns + decisions → workflows
3. Process in batches of 2-3 topics at a time (parallel Explore agents)
4. After each batch:
   - Write all raw files
   - Update INDEX.md and .kb-meta.json
   - **Run audit subagent** (step 6 above)
   - Fix any moderate+ issues
   - Report progress: "[N] of [M] topics researched"
5. Continue until all topics are done

Between batches, briefly report what was found and any surprising discoveries.

## Quality Guidelines

**Good raw research includes:**
- Specific file paths and line numbers for every claim
- Analysis of WHY, not just description of WHAT
- Connections between components (how they work together)
- Non-obvious behaviors and edge cases
- External references for patterns and best practices
- Open questions where things are unclear

**Bad raw research looks like:**
- Paraphrasing README or existing docs
- Listing files without explaining what they do
- Surface-level descriptions ("this file handles X")
- No source attribution
- No analysis or pattern identification

## Important

- Raw research is the source of truth — be thorough over concise
- Every claim must be traceable to a source file and line range
- Include external/academic references where relevant
- Note open questions honestly — gaps are valuable to document
- Don't distill or summarize for LLM consumption — that's `/skill:distill-kb`'s job
- If a topic turns out to be trivial (well-covered by existing docs), note that and suggest removing it from the KB
