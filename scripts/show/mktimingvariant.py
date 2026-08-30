#!/usr/bin/env python3
"""Compile a signal-timing dial JSON into a bottleneck-town scenario variant.

    python3 scripts/show/mktimingvariant.py design.json
    python3 scripts/show/mktimingvariant.py design.json --base data/pods/bottleneck-town/base

The dial JSON is what the game page downloads (viz/public/signal-game.html):

    {"name": "guest-r1",
     "junctions": {"J1": {"greens": [30, 8, 11, 11], "offset": 0},
                   "J2": {"greens": [36, 8, 11, 11], "offset": 12}}}

`greens` are the four green-phase durations in phase order (network.json
phase indices 0, 3, 6, 9: Main St through+right, Main St protected lefts,
cross street from the south leg, cross street from the north leg). Amber
(3 s) and all-red (2 s) stay at base values, so a junction's cycle is
sum(greens) + 20. `offset` is seconds (SUMO semantics: the first green
begins that many seconds into the sim); it is omitted from network.json
when 0, exactly as the authored arms omit it (green-wave's J1). Junctions
absent from the dial keep base values.

Output is a scenario dir data/pods/bottleneck-town/variants/<name>/ next to
the authored arms: demand/ and metrics/ copied from base verbatim (same
demand is the fairness story — only the timing may differ), a patched
network.json, and a scenario.yaml that keeps base's seed/ticks/
adaptive_routing pins. Stdlib only; no netconvert, no netimport — the
authored arms differ from base only in these same signal fields, so
patching base's JSON byte-compares clean against them.
"""
import argparse
import json
import re
import shutil
import sys
from pathlib import Path

# Green phases sit at indices 0, 3, 6, 9 of each signal's 12-phase program
# ([green, amber, all-red] x 4 movement groups), per
# scripts/demos/bottleneck_town.py's PHASE_ORDER.
GREEN_PHASES = (0, 3, 6, 9)
GREEN_LABELS = ("main through+right", "main protected lefts",
                "cross from-south (northbound)", "cross from-north (southbound)")

GREEN_MIN, GREEN_MAX = 5, 90        # seconds, per green phase
CYCLE_MIN, CYCLE_MAX = 40, 150      # seconds, per junction (greens + 20 fixed)
NAME_RE = re.compile(r"[a-z0-9](?:[a-z0-9-]*[a-z0-9])?")

# The published arms' leaderboard rows. A guest design submitted under one of
# these reuses the baseline's cache slug and silently overwrites its row
# (external review, 2026-08-29). Kept here so this validator and
# serve-baked.py's runner share one list.
RESERVED_NAMES = ("base", "retime-short", "green-wave")

REPO = Path(__file__).resolve().parents[2]
DEFAULT_BASE = REPO / "data/pods/bottleneck-town/base"
DEFAULT_OUT_ROOT = REPO / "data/pods/bottleneck-town/variants"

NOTE_TEMPLATE = ("Bottleneck Town — {name}. 4 signalized cross-street "
                 "junctions on a bowed main street; {cycle_desc}. FICTITIOUS. "
                 "Authored by hand, not imported: this geometry corresponds "
                 "to no real road anywhere.")


def fail(msg):
    print(f"mktimingvariant: error: {msg}", file=sys.stderr)
    sys.exit(2)


def is_num(x):
    # bool is an int subclass; True is not a green duration.
    return isinstance(x, (int, float)) and not isinstance(x, bool)


def fmt_s(x):
    """36.0 -> '36', 38.4 -> '38.4' (the authored arms' :g style)."""
    return f"{x:g}"


def _dir_bytes_equal(a, b):
    """Same files with the same bytes at any depth (demand/metrics are
    small trees; read them, don't trust names)."""
    af = {p.relative_to(a): p for p in a.rglob("*") if p.is_file()}
    bf = {p.relative_to(b): p for p in b.rglob("*") if p.is_file()}
    if af.keys() != bf.keys():
        return False
    return all(af[k].read_bytes() == bf[k].read_bytes() for k in af)


def load_dial(path):
    try:
        with open(path) as f:
            dial = json.load(f)
    except (OSError, json.JSONDecodeError) as e:
        fail(f"cannot read dial JSON {path}: {e}")
    if not isinstance(dial, dict):
        fail("dial JSON must be an object")
    return dial


def validate(dial, base_ids):
    """Dial -> {junction: {"greens": [...], "offset": float}}; exits on error."""
    name = dial.get("name")
    if not isinstance(name, str) or not NAME_RE.fullmatch(name):
        fail(f"name {name!r} is not slug-safe: lowercase [a-z0-9-], "
             "no leading/trailing dash (e.g. \"guest-r1\")")
    if name in RESERVED_NAMES:
        fail(f"name {name!r} is a published baseline — guest designs need "
             "a fresh name (the leaderboard row would be overwritten)")
    junctions = dial.get("junctions", {})
    if not isinstance(junctions, dict):
        fail('"junctions" must be an object mapping J1..J4 to dials')
    out = {}
    for jid, spec in junctions.items():
        if jid not in base_ids:
            fail(f"unknown junction id {jid!r} — this network has "
                 f"{', '.join(sorted(base_ids))}")
        if not isinstance(spec, dict):
            fail(f"{jid}: dial must be an object with \"greens\"/\"offset\"")
        greens = spec.get("greens")
        if (not isinstance(greens, list) or len(greens) != 4
                or not all(is_num(g) for g in greens)):
            fail(f"{jid}: \"greens\" must be 4 numbers "
                 f"(phase order: {', '.join(GREEN_LABELS)})")
        for label, g in zip(GREEN_LABELS, greens):
            if not GREEN_MIN <= g <= GREEN_MAX:
                fail(f"{jid}: {label} green {fmt_s(g)} s is outside "
                     f"[{GREEN_MIN}, {GREEN_MAX}] s")
        # Amber 3 s + all-red 2 s per movement group are untouched, so the
        # cycle is the four greens plus 20 s of fixed clearance.
        cycle = sum(greens) + 4 * (3.0 + 2.0)
        if not CYCLE_MIN <= cycle <= CYCLE_MAX:
            fail(f"{jid}: cycle {fmt_s(cycle)} s is outside "
                 f"[{CYCLE_MIN}, {CYCLE_MAX}] s (greens + 20 s amber/all-red)")
        offset = spec.get("offset", 0)
        if not is_num(offset):
            fail(f"{jid}: \"offset\" must be a number of seconds")
        if not 0 <= offset < cycle:
            fail(f"{jid}: offset {fmt_s(offset)} s is outside "
                 f"[0, {fmt_s(cycle)}) — offsets wrap at the cycle length")
        out[jid] = {"greens": [float(g) for g in greens],
                    "offset": float(offset), "cycle": cycle}
    return name, out


def patch_network(base_net, name, dials):
    """Base network with the dialed greens/offsets; everything else untouched."""
    net = json.loads(json.dumps(base_net))  # deep copy via stdlib
    cycles = {}
    for sig in net["signals"]:
        jid = sig["id"]
        d = dials.get(jid)
        if d is None:
            cycles[jid] = sum(p["duration"] for p in sig["phases"])
            continue
        for idx, g in zip(GREEN_PHASES, d["greens"]):
            sig["phases"][idx]["duration"] = g
        # Mirror the authored arms: no offset key when the offset is 0
        # (green-wave's J1 has none).
        if d["offset"]:
            sig["offset"] = round(d["offset"], 2)
        else:
            sig.pop("offset", None)
        cycles[jid] = d["cycle"]
    net["name"] = f"bottleneck-town-{name}"
    net["provenance"]["notes"] = NOTE_TEMPLATE.format(
        name=name, cycle_desc=cycle_desc(cycles))
    return net, cycles


def cycle_desc(cycles):
    """'cycle 86 s' when uniform; otherwise spell out each junction."""
    ids = sorted(cycles)
    vals = [cycles[i] for i in ids]
    if len(set(vals)) == 1:
        return f"cycle {fmt_s(vals[0])} s"
    return ("cycles " + "/".join(fmt_s(v) for v in vals)
            + f" s ({'–'.join((ids[0], ids[-1]))})")


def patch_scenario(base_yaml, name, cycles):
    """Base scenario.yaml with a new id and arm comment; pins preserved."""
    text, n = re.subn(r"^id: .*$", f"id: {name}", base_yaml, flags=re.M)
    if n != 1:
        fail("base scenario.yaml has no single 'id:' line to replace")
    text, n = re.subn(r"^# arm: .*$",
                      f"# arm: {name}; signal {cycle_desc(cycles)}",
                      text, count=1, flags=re.M)
    if n != 1:
        fail("base scenario.yaml has no '# arm:' comment to replace")
    return text


def main():
    ap = argparse.ArgumentParser(
        description="Compile a signal-timing dial JSON into a "
                    "bottleneck-town scenario variant.")
    ap.add_argument("dial", help="dial JSON file (see module docstring)")
    ap.add_argument("--base", default=str(DEFAULT_BASE),
                    help="base scenario dir (default %(default)s)")
    ap.add_argument("--out-root", default=str(DEFAULT_OUT_ROOT),
                    help="parent dir for variants (default %(default)s)")
    args = ap.parse_args()

    base = Path(args.base)
    try:
        with open(base / "network.json") as f:
            base_net = json.load(f)
        with open(base / "scenario.yaml") as f:
            base_yaml = f.read()
    except OSError as e:
        fail(f"cannot read base scenario at {base}: {e}")
    base_ids = [s["id"] for s in base_net.get("signals", [])]
    if not base_ids:
        fail(f"{base / 'network.json'} has no signals — wrong base?")

    dial = load_dial(args.dial)
    name, dials = validate(dial, set(base_ids))
    net, cycles = patch_network(base_net, name, dials)
    scenario = patch_scenario(base_yaml, name, cycles)
    net_text = json.dumps(net, separators=(",", ":"))

    outdir = Path(args.out_root) / name
    if outdir.exists():
        # Same dial, same output: re-running a guest's design is fine. A
        # DIFFERENT timing under a taken name must fail loudly — silently
        # reusing or overwriting it would score one design under another's
        # name on the leaderboard. The copied demand/metrics trees compare
        # too: after a base demand change a variant must rebuild, not
        # silently reuse — the fairness contract is "only timing changes".
        try:
            same = ((outdir / "network.json").read_text() == net_text
                    and (outdir / "scenario.yaml").read_text() == scenario
                    and _dir_bytes_equal(outdir / "demand", base / "demand")
                    and _dir_bytes_equal(outdir / "metrics", base / "metrics"))
        except OSError:
            same = False
        if not same:
            fail(f"{outdir} already exists with different contents — "
                 "pick a new name (or remove it by hand if it is truly dead)")
        print(f"mktimingvariant: reusing {outdir} (identical dial)")
    else:
        outdir.mkdir(parents=True)
        shutil.copytree(base / "demand", outdir / "demand")
        shutil.copytree(base / "metrics", outdir / "metrics")
        (outdir / "network.json").write_text(net_text)
        (outdir / "scenario.yaml").write_text(scenario)
        print(f"mktimingvariant: wrote {outdir}")

    print(f"variant {name}:")
    for jid in sorted(cycles):
        d = dials.get(jid)
        off = fmt_s(d["offset"]) + " s" if d and d["offset"] else "—"
        tag = "" if d else " (base timing)"
        print(f"  {jid}: cycle {fmt_s(cycles[jid])} s, offset {off}{tag}")


if __name__ == "__main__":
    main()
