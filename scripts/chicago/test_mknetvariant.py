#!/usr/bin/env python3
"""Tests for mknetvariant.py's repeated --add-lane (widen-by-two).

A second widening of the same corridor must stack on the FIRST pass's
clones — the corridor label map never learns clone ids, so grouping only
labelled lanes would re-donor the original outermost lane and mint a
duplicate `{id}_w1`. These tests pin the invariants the upgrade scenarios
rely on: unique ids, increasing edgeIndex, cumulative lateral offset, and
second-clone feeder wiring (an added lane nothing points at is a no-op
with a plausible label — measured 4.8% of corridor VMT before the wiring
existed).
"""
import json
import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "mknetvariant.py")

LANE_W = 3.2


def lane(lid, edge, idx, x=0.0, succ=()):
    # A 100 m straight edge heading +y; lateral offset is along x.
    return {"id": lid, "edge": edge, "edgeIndex": idx, "width": LANE_W,
            "speedLimit": 22.2,
            "shape": [[x, 0.0], [x, 100.0]],
            "successors": list(succ)}


def network():
    return {"name": "toy", "lanes": [
        lane("up_0", "up", 0, x=-50.0, succ=["e1_0", "e1_1"]),
        lane("e1_0", "e1", 0, x=0.0, succ=["e2_0", "e2_1"]),
        lane("e1_1", "e1", 1, x=LANE_W, succ=["e2_0", "e2_1"]),
        lane("e2_0", "e2", 0, x=0.0),
        lane("e2_1", "e2", 1, x=LANE_W),
        # An internal lane (no edge key) — must never match a group.
        {"id": "j:int_0", "shape": [[0, 0], [1, 1]], "successors": ["e2_0"]},
    ]}


def corridors():
    return {"labels": {"kennedy": "Kennedy"},
            "lanes": {"e1_0": "kennedy", "e1_1": "kennedy",
                      "e2_0": "kennedy", "e2_1": "kennedy"}}


def run(add_lanes):
    d = tempfile.mkdtemp()
    net, cor, out = (os.path.join(d, p) for p in ("n.json", "c.json", "o.json"))
    with open(net, "w") as f:
        json.dump(network(), f)
    with open(cor, "w") as f:
        json.dump(corridors(), f)
    args = [sys.executable, SCRIPT, "--network", net, "--corridors", cor,
            "--out", out]
    for name in add_lanes:
        args += ["--add-lane", name]
    r = subprocess.run(args, capture_output=True, text=True)
    if r.returncode != 0:
        raise AssertionError(r.stdout + r.stderr)
    with open(out) as f:
        return json.load(f)


class TestRepeatedAddLane(unittest.TestCase):
    def test_second_pass_stacks_on_the_first(self):
        net = run(["kennedy", "kennedy"])
        lanes = {L["id"]: L for L in net["lanes"]}
        # Unique ids (the duplicate-_w1 failure mode), and the chain
        # outer -> _w1 -> _w1_w1 exists on every corridor edge.
        self.assertEqual(len(lanes), len(net["lanes"]))
        for e in ("e1", "e2"):
            self.assertIn(f"{e}_1_w1", lanes)
            self.assertIn(f"{e}_1_w1_w1", lanes)
            w1, w2 = lanes[f"{e}_1_w1"], lanes[f"{e}_1_w1_w1"]
            self.assertEqual(w1["edgeIndex"], 2)
            self.assertEqual(w2["edgeIndex"], 3)
            # Cumulative offset: each clone one lane width further out than
            # its donor (the tool offsets toward +x for a +y edge).
            x1 = w1["shape"][0][0]
            x2 = w2["shape"][0][0]
            self.assertAlmostEqual(x1, 2 * LANE_W, places=6)
            self.assertAlmostEqual(x2, 3 * LANE_W, places=6)

    def test_second_clone_is_wired_in(self):
        # The junction-side half of widening: whatever feeds the donor must
        # feed the clone, on BOTH passes, or arrivals cannot enter the added
        # lanes and the scenario measures a quarter of its own widening.
        net = run(["kennedy", "kennedy"])
        lanes = {L["id"]: L for L in net["lanes"]}
        # up_0 feeds both e1 lanes; after two passes it must reach both e1
        # clones as well.
        up_succ = lanes["up_0"]["successors"]
        self.assertIn("e1_1_w1", up_succ)
        self.assertIn("e1_1_w1_w1", up_succ)
        # e1's lanes feed both e2 lanes; after two passes they must also
        # reach both e2 clones.
        for lid in ("e1_0", "e1_1", "e1_1_w1"):
            succ = lanes[lid]["successors"]
            self.assertIn("e2_1_w1", succ, lid)
            self.assertIn("e2_1_w1_w1", succ, lid)
        # The internal lane keeps its own successors untouched.
        self.assertEqual(lanes["j:int_0"]["successors"], ["e2_0"])

    def test_single_pass_is_unchanged_in_shape(self):
        # The pre-existing widen-by-one behavior must not drift: one clone
        # per corridor edge, named {outer}_w1, same successors as its donor.
        net = run(["kennedy"])
        lanes = {L["id"]: L for L in net["lanes"]}
        self.assertEqual(len(net["lanes"]), 8)
        w1 = lanes["e1_1_w1"]
        self.assertEqual(w1["edgeIndex"], 2)
        self.assertEqual(w1["successors"], lanes["e1_1"]["successors"])
        self.assertEqual(w1["source"].get("synthetic"), "add-lane")


if __name__ == "__main__":
    unittest.main()
