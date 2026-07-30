#!/usr/bin/env python3
"""Tests for mkzones.py's geometry.

The point-in-polygon test is the whole file: every district share, and every
"where is the congestion" answer, is downstream of it. Two properties matter
more than the rest, because both fail silently:

  * a point on a shared boundary must land in exactly ONE district, or the
    shares stop summing to 1 and no error is raised;
  * holes must be honoured, or a district silently absorbs the one it
    surrounds.

The projection is not tested here — it is pyproj's, and the network file
supplies the parameters — but the REFUSAL to guess it when the network
carries none is, because that failure produces a plausible-looking file with
every lane in the wrong district.
"""
import json
import os
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import mkzones                                                # noqa: E402

SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "mkzones.py")

UNIT = [[0.0, 0.0], [1.0, 0.0], [1.0, 1.0], [0.0, 1.0], [0.0, 0.0]]


class TestInRing(unittest.TestCase):
    def test_interior_and_exterior(self):
        self.assertTrue(mkzones.in_ring((0.5, 0.5), UNIT))
        self.assertFalse(mkzones.in_ring((1.5, 0.5), UNIT))
        self.assertFalse(mkzones.in_ring((-0.5, 0.5), UNIT))
        self.assertFalse(mkzones.in_ring((0.5, 1.5), UNIT))

    def test_shared_vertical_edge_claims_a_point_exactly_once(self):
        # Two districts meeting at x=1. A point on the seam must belong to
        # exactly one of them; belonging to both inflates every share and
        # belonging to neither loses the lane.
        left = UNIT
        right = [[1.0, 0.0], [2.0, 0.0], [2.0, 1.0], [1.0, 1.0], [1.0, 0.0]]
        hits = sum(mkzones.in_ring((1.0, 0.5), r) for r in (left, right))
        self.assertEqual(hits, 1)

    def test_shared_horizontal_edge_claims_a_point_exactly_once(self):
        lower = UNIT
        upper = [[0.0, 1.0], [1.0, 1.0], [1.0, 2.0], [0.0, 2.0], [0.0, 1.0]]
        hits = sum(mkzones.in_ring((0.5, 1.0), r) for r in (lower, upper))
        self.assertEqual(hits, 1)

    def test_vertex_on_the_scan_line_counts_once(self):
        # A diamond: the scan line through y=1 passes exactly through two
        # opposite vertices. A naive test double-counts them and reports the
        # interior as outside.
        diamond = [[1.0, 0.0], [2.0, 1.0], [1.0, 2.0], [0.0, 1.0], [1.0, 0.0]]
        self.assertTrue(mkzones.in_ring((1.0, 1.0), diamond))
        self.assertFalse(mkzones.in_ring((2.5, 1.0), diamond))


class TestInPolygon(unittest.TestCase):
    def test_hole_is_excluded(self):
        outer = [[0.0, 0.0], [10.0, 0.0], [10.0, 10.0], [0.0, 10.0], [0.0, 0.0]]
        hole = [[4.0, 4.0], [6.0, 4.0], [6.0, 6.0], [4.0, 6.0], [4.0, 4.0]]
        self.assertTrue(mkzones.in_polygon((1.0, 1.0), [outer, hole]))
        self.assertFalse(mkzones.in_polygon((5.0, 5.0), [outer, hole]))

    def test_empty_coords_is_not_a_match(self):
        self.assertFalse(mkzones.in_polygon((0.0, 0.0), []))


class TestMidpoint(unittest.TestCase):
    def test_midpoint_of_a_two_point_shape(self):
        # The MIDDLE of the lane, not its end. This assertion previously read
        # (4, 0) — the endpoint — because midpoint() returned s[len(s) // 2],
        # which for a two-point shape is s[1]. The test pinned the bug, which
        # is how it survived: a two-point lane crossing a district boundary
        # was filed under the district it arrived in.
        self.assertEqual(mkzones.midpoint({"shape": [[0, 0], [4, 0]]}), (2.0, 0))

    def test_midpoint_is_by_arclength_not_by_vertex_count(self):
        # Vertices bunch where a road bends. Here four of five points sit in
        # the first 10 m of a 1,010 m lane, so the middle VERTEX is at x=10
        # while the arclength midpoint is at x=505 — different districts on
        # any real boundary.
        shape = [[0, 0], [3, 0], [6, 0], [10, 0], [1010, 0]]
        self.assertEqual(mkzones.midpoint({"shape": shape}), (505.0, 0))

    def test_single_point_and_degenerate_shapes_do_not_divide_by_zero(self):
        self.assertEqual(mkzones.midpoint({"shape": [[7, 8]]}), (7, 8))
        # Every point identical: zero total length, no midpoint to find.
        self.assertEqual(mkzones.midpoint({"shape": [[2, 2], [2, 2]]}), (2, 2))

    def test_no_shape_is_none(self):
        self.assertIsNone(mkzones.midpoint({"shape": []}))
        self.assertIsNone(mkzones.midpoint({}))


class TestRefusals(unittest.TestCase):
    def _run(self, net, zones=None):
        d = tempfile.mkdtemp()
        np_ = os.path.join(d, "n.json")
        zp = os.path.join(d, "z.geojson")
        with open(np_, "w") as f:
            json.dump(net, f)
        with open(zp, "w") as f:
            json.dump(zones or {"type": "FeatureCollection", "features": []}, f)
        return subprocess.run(
            [sys.executable, SCRIPT, "--network", np_, "--zones", zp,
             "--out", os.path.join(d, "o.json")],
            capture_output=True, text=True)

    def test_network_without_projection_is_refused(self):
        r = self._run({"name": "t", "lanes": [], "provenance": {}})
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("no projection", r.stderr)

    def test_zones_with_no_matching_kind_is_refused(self):
        # An empty result here means every lane silently has no district, and
        # every share downstream becomes 0 with no error.
        net = {"name": "t", "lanes": [],
               "provenance": {"projection": "+proj=utm +zone=16 +datum=WGS84",
                              "netOffset": [0, 0]}}
        r = self._run(net, {"type": "FeatureCollection", "features": [
            {"type": "Feature",
             "properties": {"name": "x", "kind": "corridor"},
             "geometry": {"type": "Polygon", "coordinates": [UNIT]}}]})
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("no features of kind", r.stderr)


if __name__ == "__main__":
    unittest.main()
