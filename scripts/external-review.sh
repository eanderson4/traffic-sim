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
#   scripts/external-review.sh --kimi     # substitute Kimi K3 for Fable
#
# --kimi fills the non-OpenAI reviewer slot with Kimi K3 when Fable is
# unavailable (ADR-0013 addendum 2026-07-27). It preserves the two-family
# rule; it is a substitution, not a third reviewer, and the archive names
# the model that actually reviewed. Kimi has no read-only tool mode — in
# -p mode it creates files without asking — so it is pointed at a
# read-only checkout of the STAGED tree instead of the live repo. That is
# a guardrail, not a sandbox: it makes the reflexive write fail loudly in
# the reviewer's own working directory rather than mutate the repo.
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
alt=fable # the non-OpenAI reviewer slot: fable | kimi
while [ $# -gt 0 ]; do
  case "$1" in
    --gemini) with_gemini=1 ;;
    --kimi) alt=kimi ;;
    *) echo "usage: external-review.sh [--gemini] [--kimi]" >&2; exit 2 ;;
  esac
  shift
done

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
# chmod first: the --kimi tree is checked out read-only (dirs included),
# and rm cannot unlink out of a directory it cannot write.
cleanup() { chmod -R u+w "$work" 2>/dev/null || true
            [ "$keep" = 0 ] && rm -rf "$work" || echo "external-review: artifacts kept in $work" >&2; }
trap cleanup EXIT
# Inputs live in their own directory because it is the ONLY one a reviewer
# is granted, and it is made read-only before any reviewer starts. The
# reviewers' own output files stay in $work, which they are never handed:
# one reviewer able to write another's review file would let a single model
# supply both halves of a two-family gate, and the completion check (size +
# terminal marker) cannot tell an overwritten review from a real one. The
# patch is separately protected by the drift check, but sol.md has no such
# backstop.
mkdir "$work/in"
git -c core.quotePath=false diff --cached --binary --full-index --no-ext-diff > "$work/in/diff.patch"
git diff --cached --stat > "$work/in/diffstat.txt"
dh=$(git hash-object "$work/in/diff.patch" | cut -c1-8)
runid="$ts-$dh-$$"

cat > "$work/in/brief.md" <<EOF
You are reviewing a staged, UNCOMMITTED change in a traffic-simulation repo
(deterministic Go engine, NATS contract, ADR-driven; project rules in
AGENTS.md — determinism discipline ADR-0005, message contract ADR-0006,
message contracts are sacred).

The exact staged diff is at $work/in/diff.patch (stat: $work/in/diffstat.txt).
Read the diff first, then whatever repo files you need for context (the
patch — not the working tree — is what ships).

Your working directory is either the repo itself or a read-only checkout
of the staged tree; either way it shows the files under review and the
patch is the authority. Do not attempt to modify anything.

Report findings cited file:line, ranked: blocker / should-fix / nit /
question. Focus: correctness bugs; determinism risks (Go map iteration in
sampled paths, non-associative float accumulation, wall clock, RNG
discipline); contract/consistency issues (subject or payload changes need
an ADR note); design weaknesses relative to the repo's own ADRs. Review
only — DO NOT modify files. Be terse; padding wastes the committer's time.
Finish with a final line containing exactly: REVIEW-COMPLETE
EOF

echo "external-review: reviewing $(wc -l < "$work/in/diffstat.txt") stat lines of staged diff..."

chmod -R a-w "$work/in" # inputs are frozen for the duration of the round

if [ "$alt" = kimi ]; then
  # Kimi has no --allowedTools equivalent and writes files unasked in -p
  # mode, so it reviews a read-only checkout of the exact staged tree
  # rather than the live repo: same content, and a write attempt fails
  # with EACCES in its own cwd instead of touching the working tree.
  # --add-dir grants read access to the patch — $work/in, which is also
  # read-only, and NOT $work, which holds the other reviewer's output.
  # pipefail for this pipeline only: git archive failing into a successful
  # tar would hand the reviewer an empty tree, and a review of nothing
  # still ends with the marker that stamps the gate.
  mkdir "$work/tree"
  ( set -o pipefail; git archive --format=tar "$tree" | tar -x -C "$work/tree" )
  chmod -R a-w "$work/tree"
  ( cd "$work/tree" && exec timeout 900 kimi -m kimi-code/k3 --add-dir "$work/in" \
      -p "$(cat "$work/in/brief.md")" ) > "$work/kimi.md" 2> "$work/kimi.err" &
else
  timeout 900 claude -p --model fable --allowedTools "Read,Grep,Glob,Bash(git show:*),Bash(git log:*),Bash(git diff:*),Bash(cat:*),Bash(ls:*)" \
    < "$work/in/brief.md" > "$work/fable.md" 2> "$work/fable.err" &
fi
pid_alt=$!
timeout 900 codex exec -m gpt-5.6-sol --sandbox read-only "$(cat "$work/in/brief.md")" \
  > "$work/sol.md" 2> "$work/sol.err" &
pid_sol=$!
if [ "$with_gemini" = 1 ]; then
  timeout 900 gemini -p "$(cat "$work/in/brief.md")" > "$work/gemini.md" 2> "$work/gemini.err" &
  pid_gemini=$!
fi

rc=0
wait $pid_alt || rc=1
wait $pid_sol || rc=1
[ "$with_gemini" = 1 ] && { wait $pid_gemini || rc=1; }

reviewers="$alt sol"
[ "$with_gemini" = 1 ] && reviewers="$reviewers gemini"
for r in $reviewers; do
  # Completion = substantive output AND the marker as the final nonblank
  # line (ADR-0013 §4 fail-closed): a truncated review, an empty one, or
  # output that merely QUOTED the brief's instruction must not pass.
  # Leading indent and a leading "• " are stripped first: those are the
  # CLI's own rendering of a message, not something the model wrote, and
  # kimi indents every line of its final block.
  if [ ! -s "$work/$r.md" ] || [ "$(wc -c < "$work/$r.md")" -lt 50 ] || \
     [ "$(awk '{sub(/^[ \t]*(• )?/,""); sub(/[ \t\r]+$/,"")} NF{last=$0} END{print last}' "$work/$r.md")" != "REVIEW-COMPLETE" ]; then
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
if ! git -c core.quotePath=false diff --cached --binary --full-index --no-ext-diff | cmp -s "$work/in/diff.patch" -; then
  echo "external-review: the staged diff CHANGED while the reviewers ran — re-stage and re-run; not stamping" >&2
  exit 1
fi

archive="$root/docs/kb/raw/reviews"
mkdir -p "$archive"
# The archive names the model that actually reviewed — a round covered by
# the substitute must not read later as a round Fable saw.
case "$alt" in
  fable) alt_dst=claude-fable.md ;;
  kimi)  alt_dst=kimi-k3.md ;;
esac
for f in in/diff.patch:reviewed.patch in/diffstat.txt:diffstat.txt in/brief.md:brief.md "$alt.md:$alt_dst" sol.md:gpt-5.6-sol.md; do
  src=${f%%:*}; dst=${f##*:}
  cp "$work/$src" "$archive/$runid-$dst"
done
[ "$with_gemini" = 1 ] && cp "$work/gemini.md" "$archive/$runid-gemini.md"

echo "$base $tree" > "$gitdir/external-review-stamp"
keep=0 # archived and stamped: the workdir is disposable

echo "external-review: reviews + reviewed patch archived to docs/kb/raw/reviews/$runid-*; (base, tree) stamped."
echo "(Archive files are untracked — commit them separately; docs-only commits pass the gate.)"
echo "--- ${alt} ---"; cat "$work/$alt.md"
echo "--- Sol ---"; cat "$work/sol.md"
if [ "$with_gemini" = 1 ]; then echo "--- Gemini ---"; cat "$work/gemini.md"; fi
echo "external-review: if the reviews are clean (or findings fixed AND re-staged AND re-run), commit now."
