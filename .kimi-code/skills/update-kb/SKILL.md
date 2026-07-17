---
name: update-kb
description: Check knowledge base freshness — detect stale topics, gaps, and broken references
type: prompt
whenToUse: When returning to the project after a gap, or when the user asks to check KB freshness
---

# Update Knowledge Base

Detect stale research, identify gaps from recent code changes, and optionally refresh affected topics.

## Target

$ARGUMENTS

## Instructions

### 1. Load KB State

Read `docs/kb/.kb-meta.json` and `docs/kb/INDEX.md`.

If KB doesn't exist, tell the user to run `/skill:init-kb` first.

### 2. Staleness Check

For each raw research file in `.kb-meta.json`, check if source files have changed:

```bash
# For each source file recorded in the topic's metadata:
git log --oneline [recorded_git_hash]..HEAD -- [source_file_path]
```

If any source file has commits since the research was written, the topic is **stale**.

Categorize each topic:
- **Fresh**: No source files changed since research
- **Stale**: One or more source files changed
- **Missing**: Topic in INDEX.md but no raw file exists

### 3. Gap Detection

Find source files with high recent churn that aren't covered by any KB topic:

```bash
# Files with most commits in last 3 months
git log --since='3 months ago' --name-only --pretty=format: | sort | uniq -c | sort -rn | head -30
```

Cross-reference against source files listed in all raw research files. Flag high-churn files with no KB coverage as **gaps**.

### 4. Link Validation

Check all markdown links in `docs/kb/`:
- Links in INDEX.md → verify target files exist
- Links in articles → verify raw research files exist
- Links in raw files → verify source files still exist at referenced paths
- Cross-article links → verify targets exist

Flag broken links.

### 5. Generate Health Report

```markdown
## KB Health Report

**Generated:** [date]
**Topics:** [N] total

### Freshness
| Status | Count | Topics |
|--------|-------|--------|
| Fresh | [N] | [topic1, topic2, ...] |
| Stale | [N] | [topic3 (5 commits), topic4 (2 commits), ...] |
| Missing | [N] | [topic5, ...] |

### Stale Details
| Topic | Changed Files | Commits Since Research |
|-------|--------------|----------------------|
| [topic] | `path/file.ts` | [N] commits |

### Gaps (High-Churn, No Coverage)
| File | Commits (3mo) | Suggested Topic |
|------|--------------|-----------------|
| `path/new-feature.ts` | 14 | [suggested name] |

### Broken Links
- `INDEX.md:15` → `raw/missing-file.md` (not found)
- ...

### Recommendations
1. [Most urgent action]
2. [Second action]
3. [Third action]
```

### 6. Refresh (unless --check-only)

If `$ARGUMENTS` does NOT contain `--check-only`:

1. **Ask user** which stale topics to refresh (or all)
2. For each stale topic, re-research using the `/skill:research-topic` workflow:
   - Re-read the existing raw file to understand what was there before
   - Focus research on the changed source files
   - Update the raw file with new findings
   - Preserve findings about unchanged code
3. For gaps, ask user if they want to add new topics:
   - If yes, add to INDEX.md and .kb-meta.json
   - Research new topics
4. After refreshing raw research, suggest running `/skill:distill-kb` to update articles

### 7. Report Results

Summarize what was done:
- Topics refreshed
- New topics added
- Remaining stale topics (if user skipped some)
- Whether `/skill:distill-kb` needs to be re-run

## Important

- Always show the health report before taking action
- Never refresh without user confirmation
- `--check-only` should only read, never write
- When re-researching, preserve still-valid findings from the existing raw file
- Focus re-research on what actually changed, not full re-scan
- If many topics are stale, prioritize by: (1) number of changed commits, (2) topic complexity, (3) number of articles that derive from it
