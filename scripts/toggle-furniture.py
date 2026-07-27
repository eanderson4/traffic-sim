#!/usr/bin/env python3
"""Add/remove the baked index's "furniture" member (ADR-0023 §6.2).

The furniture file itself is left on disk either way — this only flips
whether the viz is told to fetch it, so it is a safe, instant toggle
during a live session.

    toggle-furniture.py off <bake-dir>...   # heads/bars/signs hidden
    toggle-furniture.py on  <bake-dir>...   # heads/bars/signs shown
"""
import json
import os
import sys

mode = sys.argv[1] if len(sys.argv) > 1 else ""
if mode not in ("on", "off") or len(sys.argv) < 3:
    sys.exit(__doc__)

for d in sys.argv[2:]:
    p = os.path.join(d, "index.json")
    with open(p) as f:
        idx = json.load(f)
    if mode == "off":
        idx.pop("furniture", None)
    else:
        if not os.path.exists(os.path.join(d, "furniture.geojson")):
            sys.exit(f"{d}: no furniture.geojson — run `pnpm bake-furniture` first")
        idx["furniture"] = "furniture.geojson"
    with open(p, "w") as f:
        json.dump(idx, f)
    print(f"{mode:3} {p}")
