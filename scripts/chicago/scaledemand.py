#!/usr/bin/env python3
"""Scale a subset of demand flows — the demand-side half of a what-if.

    scaledemand.py --in main.yaml --out main.yaml --scale 0.8 \\
        --class motorway_link                # ramp metering
    scaledemand.py --in main.yaml --out main.yaml --scale 0.9 --all
    scaledemand.py --in main.yaml --out main.yaml --no-trucks --all

Selection is by flow id suffix (mkod.py names portal flows
`pNNN-<osm class>`, resident flows `rNNN-...`), by explicit origin lane, or
by corridor via a corridors.json lane lookup. Unselected flows pass through
untouched.

A NOTE ON WHAT DEMAND SCALING CAN AND CANNOT MODEL
--------------------------------------------------------------------------
Lowering entry demand is a fair model of ramp metering, a cordon charge, or
a mode shift — measures that genuinely put fewer cars on the road. It is NOT
a model of anything that moves the same cars more efficiently, and it will
always look good on a speed metric because the network is carrying less.
That is why the paired harness reports throughput next to speed: an
"upgrade" that raises speed by admitting fewer vehicles is visible only if
you look at both.

Pure stdlib (the YAML written by mkod.py is a known flat subset, but this
round-trips through PyYAML when available for safety).
"""
import argparse
import json
import re
import sys

try:
    import yaml
except ImportError:
    sys.exit("scaledemand.py needs PyYAML (the engine's scenario loader has "
             "its own; this is the authoring side)")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--in", dest="src", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--scale", type=float, default=1.0)
    ap.add_argument("--no-trucks", action="store_true",
                    help="move the truck share to cars in selected flows")
    ap.add_argument("--all", action="store_true", help="select every flow")
    ap.add_argument("--class", dest="classes", action="append", default=[],
                    help="select flows whose id ends in this OSM class")
    ap.add_argument("--id-re", help="select flows whose id matches this regex")
    ap.add_argument("--origin", action="append", default=[],
                    help="select flows from this origin lane id")
    ap.add_argument("--corridor", action="append", default=[],
                    help="select flows whose origin lane is in this corridor "
                         "(needs --corridors)")
    ap.add_argument("--corridors")
    args = ap.parse_args()

    with open(args.src) as f:
        doc = yaml.safe_load(f)
    flows = doc["flows"]

    corr_lanes = set()
    if args.corridor:
        if not args.corridors:
            sys.exit("--corridor needs --corridors")
        with open(args.corridors) as f:
            cj = json.load(f)
        labels = cj.get("labels", {})
        keys = {k for k in labels
                if any(c.casefold() in k.casefold()
                       or c.casefold() in str(labels[k]).casefold()
                       for c in args.corridor)}
        if not keys:
            sys.exit(f"no corridor matches {args.corridor}; have {sorted(labels)}")
        corr_lanes = {lid for lid, k in cj["lanes"].items() if k in keys}

    origins = set(args.origin)
    id_re = re.compile(args.id_re) if args.id_re else None

    def selected(fl):
        if args.all:
            return True
        if any(fl["id"].endswith("-" + c) for c in args.classes):
            return True
        if id_re and id_re.search(fl["id"]):
            return True
        if fl["origin"] in origins:
            return True
        return fl["origin"] in corr_lanes

    n = 0
    moved = 0.0
    for fl in flows:
        if not selected(fl):
            continue
        n += 1
        # Two flow shapes in this repo: mkod.py emits time `slices`, while
        # mkdemand.py emits one flat rate. Scale whichever is present rather
        # than assuming, because assuming the wrong one silently produces an
        # unchanged variant that reads as a no-op result.
        if "slices" in fl:
            for sl in fl["slices"]:
                before = sl["veh_per_h"]
                sl["veh_per_h"] = round(before * args.scale, 3)
                moved += before - sl["veh_per_h"]
        elif "veh_per_h" in fl:
            before = fl["veh_per_h"]
            fl["veh_per_h"] = round(before * args.scale, 3)
            moved += before - fl["veh_per_h"]
        else:
            sys.exit(f"flow {fl['id']!r} has neither slices nor veh_per_h")
        if args.no_trucks and "vtypes" in fl:
            vt = fl["vtypes"]
            if vt.get("truck"):
                vt["car"] = round(vt.get("car", 0) + vt["truck"], 3)
                # DELETE the key rather than zero it: the scenario loader
                # requires every named vtype weight to be > 0 and rejects a
                # zero outright ("vtype \"truck\" weight must be > 0"). A
                # banned vehicle class is absent from the mix, not present
                # with no share of it.
                del vt["truck"]

    if n == 0:
        sys.exit("scaledemand: selection matched no flows — refusing to write "
                 "an unchanged variant that would silently read as a no-op")

    with open(args.out, "w") as f:
        f.write("# Variant of %s: scale %.3f on %d of %d flows%s\n"
                % (args.src, args.scale, n, len(flows),
                   ", trucks removed" if args.no_trucks else ""))
        yaml.safe_dump(doc, f, sort_keys=False, default_flow_style=False)
    print(f"[scaledemand] {n}/{len(flows)} flows scaled x{args.scale}"
          f"{' (trucks removed)' if args.no_trucks else ''}, "
          f"{moved:.0f} veh/h removed -> {args.out}", file=sys.stderr)


if __name__ == "__main__":
    main()
