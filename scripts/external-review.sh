#!/bin/bash
# external-review — the per-commit review gate (AGENTS.md "Review
# Workflow", ADR-0013): run Claude Fable and GPT-5.6-sol over the STAGED
# diff, archive their reviews AND the reviewed patch in
# docs/kb/raw/reviews/, and stamp (base HEAD, staged tree) so the
# pre-commit hook lets exactly that commit through.
#
# Usage:
#   scripts/external-review.sh            # review the staged diff
#   scripts/external-review.sh --gemini   # also run Gemini (milestone rounds)
#
# The stamp binds the index tree hash AND the base HEAD, both captured
# BEFORE the reviewers run (byte-complete — covers binaries, renames,
# everything — and immune to diff-config drift): anything staged or
# reset afterwards fails the gate. The gate proves review HAPPENED;
# triaging findings stays the committer's job (ADR-0013). Escape hatch:
# EXTERNAL_REVIEW_SKIP=1 git commit ...
set -eu
root=$(git rev-parse --show-toplevel)
gitdir=$(git rev-parse --git-dir) # per-worktree; .git may be a file
cd "$root"

with_gemini=0
case "${1:-}" in
  "") ;;
  --gemini) with_gemini=1 ;;
  *) echo "usage: external-review.sh [--gemini]" >&2; exit 2 ;;
esac

if git diff --cached --quiet; then
  echo "external-review: nothing staged — stage the change under review first" >&2
  exit 1
fi
case "$(git config core.hooksPath 2>/dev/null || true)" in
  hooks|./hooks|"$(pwd)/hooks") ;;
  *) echo "external-review: WARNING — core.hooksPath is not 'hooks' in this clone; the gate is not enforced here (git config core.hooksPath hooks)" >&2 ;;
esac
# Reviewers read the live tree for context while the patch is the authority;
# warn when staged files also have unstaged edits (context != what ships).
stale=$(comm -12 <(git -c core.quotePath=false diff --cached --name-only | sort) \
                 <(git -c core.quotePath=false diff --name-only | sort) || true)
if [ -n "$stale" ]; then
  echo "external-review: WARNING — staged files with further unstaged edits (reviewers see the live file, the gate sees the staged one):" >&2
  echo "$stale" | sed 's/^/  /' >&2
fi

tree=$(git write-tree) # the staged content, captured before the reviews run
base=$(git rev-parse HEAD)
ts=$(date +%Y-%m-%dT%H%M%S)
work=$(mktemp -d /tmp/external-review-XXXXXX)
keep=0 # failure: keep the workdir for debugging
cleanup() { [ "$keep" = 0 ] && rm -rf "$work" || echo "external-review: artifacts kept in $work" >&2; }
trap cleanup EXIT
git -c core.quotePath=false diff --cached --binary --full-index --no-ext-diff > "$work/diff.patch"
git diff --cached --stat > "$work/diffstat.txt"
dh=$(git hash-object "$work/diff.patch" | cut -c1-8)
runid="$ts-$dh-$$"

cat > "$work/brief.md" <<EOF
You are reviewing a staged, UNCOMMITTED change in a traffic-simulation repo
(deterministic Go engine, NATS contract, ADR-driven; project rules in
AGENTS.md — determinism discipline ADR-0005, message contract ADR-0006,
message contracts are sacred).

The exact staged diff is at $work/diff.patch (stat: $work/diffstat.txt).
Read the diff first, then whatever repo files you need for context (the
patch — not the working tree — is what ships).

Report findings cited file:line, ranked: blocker / should-fix / nit /
question. Focus: correctness bugs; determinism risks (Go map iteration in
sampled paths, non-associative float accumulation, wall clock, RNG
discipline); contract/consistency issues (subject or payload changes need
an ADR note); design weaknesses relative to the repo's own ADRs. Review
only — DO NOT modify files. Be terse; padding wastes the committer's time.
Finish with a final line containing exactly: REVIEW-COMPLETE
EOF

echo "external-review: reviewing $(wc -l < "$work/diffstat.txt") stat lines of staged diff..."

timeout 900 claude -p --model fable --allowedTools "Read,Grep,Glob,Bash(git show:*),Bash(git log:*),Bash(git diff:*),Bash(cat:*),Bash(ls:*)" \
  < "$work/brief.md" > "$work/fable.md" 2> "$work/fable.err" &
pid_fable=$!
timeout 900 codex exec -m gpt-5.6-sol --sandbox read-only "$(cat "$work/brief.md")" \
  > "$work/sol.md" 2> "$work/sol.err" &
pid_sol=$!
if [ "$with_gemini" = 1 ]; then
  timeout 900 gemini -p "$(cat "$work/brief.md")" > "$work/gemini.md" 2> "$work/gemini.err" &
  pid_gemini=$!
fi

rc=0
wait $pid_fable || rc=1
wait $pid_sol || rc=1
[ "$with_gemini" = 1 ] && { wait $pid_gemini || rc=1; }

reviewers="fable sol"
[ "$with_gemini" = 1 ] && reviewers="$reviewers gemini"
for r in $reviewers; do
  # Completion = substantive output AND the marker as the final nonblank
  # line (ADR-0013 §4 fail-closed): a truncated review, an empty one, or
  # output that merely QUOTED the brief's instruction must not pass.
  if [ ! -s "$work/$r.md" ] || [ "$(wc -c < "$work/$r.md")" -lt 50 ] || \
     [ "$(awk '{sub(/[ \t\r]+$/,"")} NF{last=$0} END{print last}' "$work/$r.md")" != "REVIEW-COMPLETE" ]; then
    echo "external-review: $r did not complete a review (missing terminal marker; see $work/$r.err) — NOT stamping" >&2
    rc=1
  fi
done
if [ "$rc" != 0 ]; then
  keep=1
  echo "external-review: a reviewer failed; not stamping" >&2
  exit 1
fi

keep=1 # reviews complete: never lose them to a late archive failure

# Drift check: the stamp covers the tree captured BEFORE the reviews ran.
if ! git -c core.quotePath=false diff --cached --binary --full-index --no-ext-diff | cmp -s "$work/diff.patch" -; then
  echo "external-review: the staged diff CHANGED while the reviewers ran — re-stage and re-run; not stamping" >&2
  exit 1
fi

archive="$root/docs/kb/raw/reviews"
mkdir -p "$archive"
for f in diff.patch:reviewed.patch diffstat.txt:diffstat.txt brief.md:brief.md fable.md:claude-fable.md sol.md:gpt-5.6-sol.md; do
  src=${f%%:*}; dst=${f##*:}
  cp "$work/$src" "$archive/$runid-$dst"
done
[ "$with_gemini" = 1 ] && cp "$work/gemini.md" "$archive/$runid-gemini.md"

echo "$base $tree" > "$gitdir/external-review-stamp"
keep=0 # archived and stamped: the workdir is disposable

echo "external-review: reviews + reviewed patch archived to docs/kb/raw/reviews/$runid-*; (base, tree) stamped."
echo "(Archive files are untracked — commit them separately; docs-only commits pass the gate.)"
echo "--- Fable ---"; cat "$work/fable.md"
echo "--- Sol ---"; cat "$work/sol.md"
if [ "$with_gemini" = 1 ]; then echo "--- Gemini ---"; cat "$work/gemini.md"; fi
echo "external-review: if the reviews are clean (or findings fixed AND re-staged AND re-run), commit now."
