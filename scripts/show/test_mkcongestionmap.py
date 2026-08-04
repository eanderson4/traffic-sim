#!/usr/bin/env python3
"""Tests for mkcongestionmap.py's measurement semantics.

Only the pure half: lane_speeds and the colour bands. Rasterising is Pillow's
job and is not what goes wrong here — what goes wrong is confusing ABSENT with
EMPTY, which produces a picture that looks like a quiet city and a coverage
check that rejects one.
"""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

# Pillow is imported at module scope by the script and is optional in a
# checkout (build-quiz.sh takes CHI_SKIP_MAP=1), so skip rather than fail.
try:
    import mkcongestionmap as m
except SystemExit:  # pragma: no cover - the script exits when Pillow is absent
    m = None


def iv(lid, begin, end, dist, time_s, set_id="net", partial=False):
    return {"set_id": set_id, "lane_id": lid, "begin_tick": begin,
            "end_tick": end, "sum_dist_m": dist, "sum_time_s": time_s,
            "partial": partial}


def write(tmp, intervals):
    import json
    p = os.path.join(tmp, "m.json")
    with open(p, "w") as f:
        json.dump({"ticks": 1000, "dt": 0.1, "intervals": intervals}, f)
    return p


@unittest.skipIf(m is None, "Pillow not installed")
class TestLaneSpeeds(unittest.TestCase):
    def test_a_measured_but_empty_lane_is_measured(self):
        # THE bug: `if t > 0` dropped this lane, so it rendered identically to
        # a lane nobody watched, and the --min-coverage numerator counted only
        # occupied lanes — a quiet run failed as though it were unmeasured.
        import tempfile
        tmp = tempfile.mkdtemp()
        path = write(tmp, [iv("busy", 0, 1000, 1000.0, 100.0),
                           iv("quiet", 0, 1000, 0.0, 0.0)])
        speeds, measured, set_id, first, last = m.lane_speeds(path, 0)
        # A lane with no vehicle-time has no mean speed to take...
        self.assertNotIn("quiet", speeds)
        self.assertIn("busy", speeds)
        # ...but it WAS measured, and that is a different fact from absent.
        self.assertEqual(measured, {"busy", "quiet"})
        self.assertAlmostEqual(speeds["busy"], 10.0, places=6)
        self.assertEqual(set_id, "net")
        self.assertEqual((first, last), (0, 1000))

    def test_a_lane_outside_the_set_is_in_neither_collection(self):
        import tempfile
        tmp = tempfile.mkdtemp()
        path = write(tmp, [iv("a", 0, 1000, 100.0, 10.0)])
        speeds, measured, _, _, _ = m.lane_speeds(path, 0)
        self.assertNotIn("never-reported", speeds)
        self.assertNotIn("never-reported", measured)

    def test_partial_intervals_are_dropped_from_both(self):
        import tempfile
        tmp = tempfile.mkdtemp()
        path = write(tmp, [iv("a", 0, 1000, 100.0, 10.0),
                           iv("p", 1000, 1200, 50.0, 5.0, partial=True)])
        speeds, measured, _, _, _ = m.lane_speeds(path, 0)
        self.assertNotIn("p", speeds)
        self.assertNotIn("p", measured)

    def test_the_warmup_cut_applies_to_measurement_too(self):
        import tempfile
        tmp = tempfile.mkdtemp()
        path = write(tmp, [iv("early", 0, 500, 100.0, 10.0),
                           iv("late", 500, 1000, 100.0, 10.0)])
        speeds, measured, _, first, _ = m.lane_speeds(path, 500)
        self.assertEqual(measured, {"late"})
        self.assertEqual(first, 500)

    def test_multiple_sets_are_refused_rather_than_blended(self):
        import tempfile
        tmp = tempfile.mkdtemp()
        path = write(tmp, [iv("a", 0, 1000, 100.0, 10.0, set_id="one"),
                           iv("a", 0, 1000, 100.0, 10.0, set_id="two")])
        with self.assertRaises(SystemExit) as cm:
            m.lane_speeds(path, 0)
        self.assertIn("measurement sets", str(cm.exception))


@unittest.skipIf(m is None, "Pillow not installed")
class TestColours(unittest.TestCase):
    def test_absent_and_empty_are_different_colours(self):
        # If these are ever equal the picture stops distinguishing "watched,
        # no traffic" from "nobody watched", which is the whole point.
        self.assertNotEqual(m.EMPTY, m.UNMEASURED)

    def test_a_stopped_lane_is_not_coloured_free_flow(self):
        stopped = m.colour_for(0.5, 20.0)     # 2.5% of the limit
        flowing = m.colour_for(20.0, 20.0)    # at the limit
        self.assertNotEqual(stopped, flowing)
        self.assertEqual(stopped, m.BANDS[0][1])
        self.assertEqual(flowing, m.BANDS[-1][1])

    def test_a_lane_with_no_posted_limit_cannot_be_rated(self):
        self.assertEqual(m.colour_for(10.0, 0.0), m.EMPTY)


if __name__ == "__main__":
    unittest.main()
