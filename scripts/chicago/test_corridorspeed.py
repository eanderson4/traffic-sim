#!/usr/bin/env python3
"""Tests for corridorspeed.py — specifically what it CLAIMS about its scope.

The tool prints per-corridor Edie speeds and one aggregate row. That row used
to be labelled "NETWORK (all lanes)" unconditionally, so a `--set` naming a
corridor-only measurement set presented that corridor's speed and VMT as a
whole-network result (ADR-0014 permits subset sets). The aggregate is over
whatever the selected set measured, and the label has to say so.
"""
import json
import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "corridorspeed.py")


def iv(lid, begin, end, dist, time_s, set_id="net", partial=False):
    return {"set_id": set_id, "lane_id": lid, "begin_tick": begin,
            "end_tick": end, "sum_dist_m": dist, "sum_time_s": time_s,
            "partial": partial}


def run(intervals, lane2c, extra=()):
    d = tempfile.mkdtemp()
    met, cor = os.path.join(d, "m.json"), os.path.join(d, "c.json")
    with open(met, "w") as f:
        json.dump({"ticks": 1000, "dt": 0.1, "intervals": intervals}, f)
    with open(cor, "w") as f:
        json.dump({"lanes": lane2c, "labels": {}}, f)
    r = subprocess.run([sys.executable, SCRIPT, met, "--corridors", cor, *extra],
                       capture_output=True, text=True)
    return r


class TestAggregateLabel(unittest.TestCase):
    def test_the_aggregate_never_claims_all_lanes(self):
        # Two lanes measured, one of them on a corridor.
        r = run([iv("a", 0, 1000, 1000.0, 100.0), iv("b", 0, 1000, 500.0, 100.0)],
                {"a": "kennedy"})
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertNotIn("NETWORK (all lanes)", r.stdout)
        self.assertIn("ALL MEASURED LANES (2)", r.stdout)

    def test_a_named_subset_set_says_it_is_a_set(self):
        # --set picks one of two sets; the aggregate covers only that set, so
        # it must not read as a network figure.
        ivs = [iv("a", 0, 1000, 1000.0, 100.0, set_id="corridor-only"),
               iv("b", 0, 1000, 500.0, 100.0, set_id="whole-net"),
               iv("c", 0, 1000, 500.0, 100.0, set_id="whole-net")]
        r = run(ivs, {"a": "kennedy"}, extra=("--set", "corridor-only"))
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertNotIn("NETWORK (all lanes)", r.stdout)
        self.assertIn("MEASURED SET (1 lanes)", r.stdout)

    def test_the_aggregate_is_edie_over_the_measured_lanes(self):
        # 1500 m over 200 s = 7.5 m/s = 27.0 km/h, and 1.5 veh-km.
        r = run([iv("a", 0, 1000, 1000.0, 100.0), iv("b", 0, 1000, 500.0, 100.0)],
                {"a": "kennedy"})
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        line = [ln for ln in r.stdout.splitlines() if "ALL MEASURED LANES" in ln][0]
        self.assertIn("27.0", line)
        self.assertIn("2", line.split()[-1])  # 1.5 -> rounds to 2 veh-km

    def test_multiple_sets_without_a_choice_are_refused(self):
        ivs = [iv("a", 0, 1000, 100.0, 10.0, set_id="one"),
               iv("a", 0, 1000, 100.0, 10.0, set_id="two")]
        r = run(ivs, {"a": "kennedy"})
        self.assertNotEqual(r.returncode, 0)

    def test_partial_intervals_do_not_reach_the_aggregate(self):
        ivs = [iv("a", 0, 1000, 1000.0, 100.0),
               iv("a", 1000, 1200, 9999.0, 1.0, partial=True)]
        r = run(ivs, {"a": "kennedy"})
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        line = [ln for ln in r.stdout.splitlines() if "ALL MEASURED LANES" in ln][0]
        self.assertIn("36.0", line)  # 1000 m / 100 s = 10 m/s
        self.assertIn("(1)", line)


if __name__ == "__main__":
    unittest.main()
