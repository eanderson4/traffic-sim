#!/usr/bin/env python3
"""Score a bottleneck-town signal-timing design and refresh the leaderboard.

    python3 scripts/show/score-timing.py --dial guest-r1.json --name guest-r1
    python3 scripts/show/score-timing.py --dial guest-r1.json --name guest-r1 --bake
    python3 scripts/show/score-timing.py --variant data/pods/bottleneck-town/variants/guest-r1 --name guest-r1
    python3 scripts/show/score-timing.py --variant data/pods/bottleneck-town/base --name base --rebase

Every row is measured the same way: engine/cmd/serve at seed 42 over the
scenario's own demand program and 15,000-tick window (`serve`, not `simrun` —
headless simrun refuses scenarios that declare demand parts; serve embeds
the run-seeded demand director, so the demand program is a pure function of
the seed and identical for every design). The curated quiz numbers under
docs/show/quiz/ are NOT reused: those were pooled over different seeds and a
warmed-up window, and fairness on camera needs one seed, one demand, one
window for baselines and guests alike.

Scores: mean time loss per vehicle (metrics.json `totals.mean_time_loss_s` —
total time loss over completed + stranded + still-active trips) and trips
completed (`totals.completed_trips`).

Repeatability, measured 2026-08-29 on an idle box: the same base scenario at
seed 42 scored 161.694–161.722 s/veh across six serial runs (trips completed
474 every time). Live-bus runs are not bit-identical — the embedded driver's
coast ticks vary with timing (below serve's 0.1% fidelity bar; see the
fidelity section of docs/show/bottleneck-town.md) — so ~±0.03 s of noise on a
~162 s score is expected and the last displayed digit can move between runs.
Gaps that matter on camera are 20+ s; if two designs land within a second of
each other, call the tie, don't re-run to break it. On a loaded box an
unpaced run can also void outright on the coasting bar: the runner retries,
then falls back to -pace 10 (~3 min) before giving up.

Runs are cached in data/runs/timing-game/<slug>.score.json (with the raw
metrics and the serve log beside them); a cached row is never re-run unless
--rebase (the three published arms) or --force (the --name'd entry). The
leaderboard viz/public/timing-game-results.json is rewritten from ALL
cached rows on every invocation, sorted by time loss ascending — so guest
entries accumulate across invocations and the baselines are always present.
On the first invocation the three published arms (base, retime-short,
green-wave) are run and cached before the guest's design is scored.

--bake is the live-game flow (serve-baked.py --timing-runner drives it):
the --name'd entry's serve run also records to /tmp/bt-store-<slug> under
run id <slug>, and cmd/bake turns that recording into the 3D-replay
artifacts at data/baked/baked/<slug>/<hash12>/ before the score row is
final — the row gains "bake": {"run", "hash"} and a bake failure is fatal
(a score without a replay is useless there). A cached row whose replay is
missing (no bake entry, or the bake dir was cleaned) is re-run from
scratch: the run IS the recording, so there is no bake-only shortcut. The
three baselines are never recorded or baked, and without --bake every
behavior above is unchanged.
"""
import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import time
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO / "scripts"))
import whatif  # FIDELITY_FAIL / TRANSIENT: serve's own wording, one source

POD = REPO / "data/pods/bottleneck-town"
BASELINES = {slug: POD / slug for slug in ("base", "retime-short", "green-wave")}
VARIANTS_ROOT = POD / "variants"
RUNS_DIR = REPO / "data/runs/timing-game"
BOARD = REPO / "viz/public/timing-game-results.json"
SERVE_BIN = Path("/tmp/tsim/serve")
BAKE_BIN = Path("/tmp/tsim/bake")
BAKED_ROOT = REPO / "data/baked"
MKVARIANT = REPO / "scripts/show/mktimingvariant.py"

SEED = 42
TICKS = 15000  # the scenario manifest's pin; verified against each metrics doc
PORT_BASE = 18600
PORT_STRIDE = 997  # whatif.py's retry stride, well clear of its 8600 block


def fail(msg, rc=1):
    print(f"score-timing: error: {msg}", file=sys.stderr)
    sys.exit(rc)


def slugify(name):
    slug = re.sub(r"[^a-z0-9-]+", "-", name.lower()).strip("-")
    if not slug:
        fail(f"--name {name!r} has no usable characters for a file slug")
    return slug


def ensure_serve():
    """Build cmd/serve once; reuse the binary while it exists."""
    if SERVE_BIN.exists():
        return
    SERVE_BIN.parent.mkdir(parents=True, exist_ok=True)
    print(f"score-timing: building {SERVE_BIN} (go build ./cmd/serve) ...")
    rc = subprocess.call(["go", "build", "-o", str(SERVE_BIN), "./cmd/serve"],
                         cwd=REPO / "engine")
    if rc != 0 or not SERVE_BIN.exists():
        fail("go build ./cmd/serve failed")


def ensure_bake():
    """Build cmd/bake once; reuse the binary while it exists."""
    ensure_serve()  # /tmp may have been wiped since the last scoring run
    if BAKE_BIN.exists():
        return
    BAKE_BIN.parent.mkdir(parents=True, exist_ok=True)
    print(f"score-timing: building {BAKE_BIN} (go build ./cmd/bake) ...")
    rc = subprocess.call(["go", "build", "-o", str(BAKE_BIN), "./cmd/bake"],
                         cwd=REPO / "engine")
    if rc != 0 or not BAKE_BIN.exists():
        fail("go build ./cmd/bake failed")


def run_serve(scenario, slug, record=False):
    """One seed-42 serve run -> parsed metrics doc.

    Attempt ladder: three unpaced (-pace 0, ~20 s) tries, then two paced
    (-pace 10, ~3 min) ones. Two retryable failure shapes: transient broker
    bind races (whatif.py's TRANSIENT list), and runs serve itself declared
    void for UNCONTROLLED COASTING only. Coasting past the bar is a load
    artifact — the embedded driver fell behind an unpaced engine on a busy
    box, not a property of the design — so re-running on a quieter moment,
    then giving the driver wall-clock headroom per tick, is the honest fix
    (the pod's fidelity notes end with "give the box to it", but on recording
    day the box runs OBS and a browser, so "quiet" is not on the menu; pace
    10 measured 0.07% coasting at load ~14/16 where pace 0 voided three
    times). The other two fidelity failures (demand under-delivery, a blind
    controller) are real run defects and stay immediately fatal.

    With record=True (the --bake flow) the run also leaves a durable
    recording for cmd/bake: store /tmp/bt-store-<slug>, run id the slug
    itself. Every attempt wipes the store first — a previous attempt's
    partial (or voided-but-complete) recording of the same run id would
    make serve refuse to append.
    """
    mpath = RUNS_DIR / f"{slug}.metrics.json"
    logp = RUNS_DIR / f"{slug}.serve.log"
    store = f"/tmp/bt-store-{slug}" if record else None
    attempts = [(0, 2), (0, 10), (0, 10), (10, 5), (10, 5)]  # (pace, sleep)
    last_coast = False
    for i, (pace, pause) in enumerate(attempts):
        if i == 3:
            print("score-timing: falling back to -pace 10 (~3 min instead of "
                  "~20 s — gives the driver headroom on a loaded box)",
                  file=sys.stderr)
        port = PORT_BASE + i * PORT_STRIDE
        rec = []
        if record:
            shutil.rmtree(store, ignore_errors=True)
            rec = ["-store", store]
        cmd = [str(SERVE_BIN), "-scenario", str(scenario),
               "-run", slug if record else f"sg{port}",
               "-seed", str(SEED), "-pace", str(pace), "-capacity", "40000",
               "-intent-log=false", "-metrics-out", str(mpath),
               "-ws", f"127.0.0.1:{port}"] + rec
        with open(logp, "w") as lf:
            rc = subprocess.call(cmd, stdout=lf, stderr=subprocess.STDOUT)
        if rc != 0 or not mpath.exists():
            tail = logp.read_text()[-2000:] if logp.exists() else ""
            if i == len(attempts) - 1 or not any(t in tail
                                                 for t in whatif.TRANSIENT):
                fail(f"serve run failed (rc={rc}); tail of {logp}:\n{tail}")
            time.sleep(pause)  # transient broker bind race: fresh port next
            continue
        problems = [why for pat, why in whatif.FIDELITY_FAIL
                    if pat in logp.read_text()]
        if not problems:
            with open(mpath) as f:
                doc = json.load(f)
            doc["_pace"] = pace  # provenance for the score cache, not metrics
            return doc
        last_coast = (len(problems) == 1 and "coasting" in problems[0])
        if last_coast and i < len(attempts) - 1:
            print(f"score-timing: attempt {i + 1} voided for coasting "
                  "(box is loaded — the driver fell behind); retrying",
                  file=sys.stderr)
            time.sleep(pause)
            continue
        fail(f"serve declared the run void — {problems[0]} "
             f"(see {logp}); its numbers would read as congestion, not design"
             + (". Every attempt void: re-run when the box is idle"
                if last_coast else ""))


def run_bake(slug):
    """Bake the recorded run -> its hash12; fatal on any failure.

    bake opens the JetStream store exclusively, so the recording serve must
    have exited first — run_serve's subprocess.call guarantees that. The
    bake lands at data/baked/baked/<slug>/<hash12>/; exactly one hash dir
    is expected, but a re-bake of a changed recording lands beside the old
    one, so take the newest by mtime.
    """
    logp = RUNS_DIR / f"{slug}.bake.log"
    with open(logp, "w") as lf:
        rc = subprocess.call([str(BAKE_BIN), "-run", slug,
                              "-store", f"/tmp/bt-store-{slug}",
                              "-out", str(BAKED_ROOT)],
                             cwd=REPO, stdout=lf, stderr=subprocess.STDOUT)
    rundir = BAKED_ROOT / "baked" / slug
    hashes = ([p for p in rundir.iterdir() if (p / "index.json").is_file()]
              if rundir.is_dir() else [])
    if rc != 0 or not hashes:
        tail = logp.read_text()[-2000:] if logp.exists() else ""
        fail(f"bake of run {slug} failed (rc={rc}) — a score without a "
             f"replay is useless in this flow; tail of {logp}:\n{tail}")
    return max(hashes, key=lambda p: p.stat().st_mtime).name


def baked_index(row):
    """The row's baked index.json if the replay is on disk, else None."""
    b = row.get("bake")
    if not isinstance(b, dict) or not b.get("run") or not b.get("hash"):
        return None
    p = BAKED_ROOT / "baked" / str(b["run"]) / str(b["hash"]) / "index.json"
    return p if p.is_file() else None


def score(name, scenario, kind, force=False, bake=False):
    """-> (slug, row); cached rows return without re-running."""
    slug = slugify(name)
    cache = RUNS_DIR / f"{slug}.score.json"
    if cache.exists() and not force:
        with open(cache) as f:
            row = json.load(f)
        if not bake or baked_index(row) is not None:
            print(f"score-timing: {name}: using cached {cache.name} "
                  f"(delete it or pass --force/--rebase to re-run)")
            return slug, row
        # --bake flow, but the cached row has no replay on disk (the bake
        # died after the row was written, or data/baked was cleaned). The
        # recording is the run itself, so a full re-run with recording on
        # is the only honest path — there is no bake-only shortcut.
        print(f"score-timing: {name}: cached row has no baked replay; "
              "re-running with recording on")
    print(f"score-timing: running {scenario} at seed {SEED} ...")
    metrics = run_serve(scenario, slug, record=bake)
    pace = metrics.pop("_pace", 0)
    totals = metrics.get("totals", {})
    if "mean_time_loss_s" not in totals or "completed_trips" not in totals:
        fail(f"{RUNS_DIR / (slug + '.metrics.json')} has no "
             "totals.mean_time_loss_s / totals.completed_trips — "
             "metrics schema changed?")
    if metrics.get("ticks") != TICKS:
        fail(f"run reported ticks={metrics.get('ticks')}, expected {TICKS} "
             "— the leaderboard compares one window only")
    row = {"name": name, "kind": kind,
           "mean_time_loss_s": totals["mean_time_loss_s"],
           "trips_completed": totals["completed_trips"],
           "scenario": os.path.relpath(scenario, REPO), "seed": SEED,
           "ticks": metrics["ticks"], "pace": pace}
    with open(cache, "w") as f:
        json.dump(row, f, indent=2)
        f.write("\n")
    if bake:
        hash12 = run_bake(slug)
        row["bake"] = {"run": slug, "hash": hash12}
        with open(cache, "w") as f:
            json.dump(row, f, indent=2)
            f.write("\n")
        print(f"score-timing: baked -> /baked/baked/{slug}/{hash12}/index.json")
    return slug, row


def rewrite_board():
    """Leaderboard = every cached row, best (lowest) time loss first."""
    rows = []
    for p in sorted(RUNS_DIR.glob("*.score.json")):
        with open(p) as f:
            r = json.load(f)
        if r.get("ticks") != TICKS or r.get("seed") != SEED:
            fail(f"{p.name} was scored at seed {r.get('seed')} / "
                 f"{r.get('ticks')} ticks — remove it; the board is "
                 f"seed {SEED}, {TICKS} ticks only")
        rows.append({"name": r["name"], "kind": r["kind"],
                     "mean_time_loss_s": round(r["mean_time_loss_s"], 2),
                     "trips_completed": r["trips_completed"]})
    rows.sort(key=lambda r: r["mean_time_loss_s"])
    board = {"generated_from": f"seed {SEED}, bottleneck-town, {TICKS} ticks",
             "rows": rows}
    with open(BOARD, "w") as f:
        json.dump(board, f, indent=2)
        f.write("\n")
    return rows


def main():
    ap = argparse.ArgumentParser(
        description="Score a signal-timing design at seed 42 and refresh "
                    "the leaderboard.")
    src = ap.add_mutually_exclusive_group(required=True)
    src.add_argument("--variant",
                     help="scenario dir to score (already built variant)")
    src.add_argument("--dial",
                     help="dial JSON file — mktimingvariant.py builds the "
                          "variant first, then it is scored")
    ap.add_argument("--name", required=True,
                    help="leaderboard label for this entry (also the cache "
                         "slug, lowercased)")
    ap.add_argument("--rebase", action="store_true",
                    help="re-run the three published arms even if cached")
    ap.add_argument("--force", action="store_true",
                    help="re-run the --name'd entry even if cached")
    ap.add_argument("--bake", action="store_true",
                    help="record the --name'd entry's run and bake it to "
                         "data/baked (the live-game 3D-replay flow); the "
                         "score row gains a bake hash, a bake failure is "
                         "fatal, and a cached row missing its replay is "
                         "re-run")
    args = ap.parse_args()

    RUNS_DIR.mkdir(parents=True, exist_ok=True)
    ensure_serve()
    if args.bake:
        ensure_bake()

    # The published arms go first: every board has them, measured through
    # this same runner, or the guest's number has nothing to be ranked
    # against.
    for slug, scen in BASELINES.items():
        if not scen.is_dir():
            fail(f"published arm missing: {scen}")
        score(slug, scen, "baseline", force=args.rebase)

    if args.dial:
        # The variant is content-addressed by name: re-running the same
        # dial reuses it, a different timing under the same name fails in
        # mktimingvariant before any sim time is spent.
        rc = subprocess.call([sys.executable, str(MKVARIANT), args.dial])
        if rc != 0:
            fail(f"mktimingvariant rejected {args.dial}", rc)
        try:
            with open(args.dial) as f:
                variant_name = json.load(f)["name"]
        except (OSError, json.JSONDecodeError, KeyError) as e:
            fail(f"cannot re-read dial name from {args.dial}: {e}")
        scenario = VARIANTS_ROOT / variant_name
    else:
        scenario = Path(args.variant)
    if not scenario.is_dir():
        fail(f"scenario dir not found: {scenario}")

    kind = "baseline" if slugify(args.name) in BASELINES else "guest"
    slug, row = score(args.name, scenario, kind, force=args.force,
                      bake=args.bake)
    rows = rewrite_board()
    rank = next(i for i, r in enumerate(rows, 1) if slugify(r["name"]) == slug)
    print(f"{args.name}: mean time loss {row['mean_time_loss_s']:.1f}s/veh, "
          f"{row['trips_completed']} trips completed "
          f"(rank {rank} of {len(rows)})")
    print(f"score-timing: leaderboard -> {BOARD}")


if __name__ == "__main__":
    main()
