#!/usr/bin/env python3
"""Tests for runreport.py — the standard run report.

The properties under test are the ones that made earlier ad-hoc analyses
wrong, not the formatting:

  * partial intervals are dropped (ADR-0014 3);
  * overlapping measurement sets are refused rather than summed;
  * density is per LANE-km of that lane, so a long lane and a short lane with
    the same vehicle count do not report the same density;
  * empty road lands in its own bucket instead of being averaged in as a zero
    speed or silently dropped;
  * lane-km shares and VMT shares are computed against different denominators
    and are allowed to disagree — that disagreement is the signal.
"""
import json
import os
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import runreport as rr                                        # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "runreport.py")


def lane(lid, length_m):
    """A straight lane of the given length, as the network file stores it.

    Carries BOTH `length` and a matching `shape`, which is what netimport
    emits. They agree here; TestLaneLength covers what happens when they
    do not, because on the real Chicago network 8.5% of lanes disagree.
    """
    return {"id": lid, "length": float(length_m),
            "shape": [[0.0, 0.0], [float(length_m), 0.0]]}


def interval(lid, begin, end, dist_m, time_s, loss=0.0, set_id="net",
             partial=False):
    return {"set_id": set_id, "lane_id": lid, "begin_tick": begin,
            "end_tick": end, "sum_dist_m": dist_m, "sum_time_s": time_s,
            "time_loss_s": loss, "partial": partial}


class TestLaneLength(unittest.TestCase):
    """The network's own `length` is authoritative; geometry is a fallback."""

    def test_length_field_wins_over_the_polyline(self):
        ln = {"id": "a", "length": 250.0,
              "shape": [[0.0, 0.0], [100.0, 0.0]]}
        self.assertEqual(rr.lane_length_m(ln), 250.0)

    def test_positive_length_with_a_degenerate_shape_is_not_zero(self):
        # 895 lanes on chi-loop-urban look exactly like this: a real 0.1 m
        # junction internal whose polyline measures 0 m. Deriving length from
        # geometry made them 0.0 km, which dropped them out of every
        # distribution and every group denominator — the survivorship bias
        # ADR-0014 3 forbids, reached by arithmetic rather than omission.
        ln = {"id": "i", "length": 0.1, "shape": [[5.0, 5.0], [5.0, 5.0]]}
        self.assertEqual(rr.lane_length_m(ln), 0.1)
        self.assertEqual(rr.lane_length_m({"id": "j", "length": 0.1,
                                           "shape": [[5.0, 5.0]]}), 0.1)

    def test_geometry_is_the_fallback_when_length_is_absent(self):
        ln = {"id": "a", "shape": [[0.0, 0.0], [3.0, 4.0]]}
        self.assertEqual(rr.lane_length_m(ln), 5.0)

    def test_non_positive_length_reports_zero_for_the_caller_to_refuse(self):
        self.assertEqual(rr.lane_length_m({"id": "a", "length": 0.0}), 0.0)
        self.assertEqual(rr.lane_length_m({"id": "a", "length": -2.0}), 0.0)


class TestLaneCentre(unittest.TestCase):
    """Hotspot coordinates: the middle of the lane, not the middle vertex."""

    def test_two_point_lane_centres_between_the_ends(self):
        self.assertEqual(rr.lane_centre({"shape": [[0, 0], [10, 0]]}),
                         (5.0, 0.0))

    def test_centre_is_by_arclength_not_by_vertex_index(self):
        # Four of five vertices in the first 10 m of a 1,010 m lane: the
        # middle vertex is at x=10, the arclength midpoint at x=505. A
        # hotspot marker on the former lands at the wrong end of the road.
        shape = [[0, 0], [3, 0], [6, 0], [10, 0], [1010, 0]]
        self.assertEqual(rr.lane_centre({"shape": shape}), (505.0, 0.0))

    def test_degenerate_shapes_do_not_divide_by_zero(self):
        self.assertEqual(rr.lane_centre({"shape": [[4, 9]]}), (4, 9))
        self.assertEqual(rr.lane_centre({"shape": [[4, 9], [4, 9]]}), (4, 9))
        self.assertIsNone(rr.lane_centre({"shape": []}))


class TestBands(unittest.TestCase):
    def test_density_band_edges_are_upper_exclusive(self):
        # 25% of critical belongs to the 25-50 band, not to <25 — otherwise
        # every boundary value is double-counted or lost depending on which
        # comparison ran first.
        self.assertEqual(rr.band_of(0.249, rr.K_BANDS), "<25%")
        self.assertEqual(rr.band_of(0.25, rr.K_BANDS), "25-50%")
        self.assertEqual(rr.band_of(1.0, rr.K_BANDS), "100-150%")
        self.assertEqual(rr.band_of(99.0, rr.K_BANDS), ">150%")

    def test_speed_band_edges(self):
        self.assertEqual(rr.band_of(0.0, rr.V_BANDS), "<10")
        self.assertEqual(rr.band_of(20.0, rr.V_BANDS), "20-30")
        self.assertEqual(rr.band_of(200.0, rr.V_BANDS), "60+")


class TestDist(unittest.TestCase):
    def test_empty_is_counted_in_lane_km_but_not_vmt(self):
        d = rr.Dist(rr.V_BANDS)
        d.add("60+", lane_km_h=1.0, vmt=70.0)
        d.add_empty(3.0)
        self.assertAlmostEqual(d.total_km, 4.0)
        self.assertAlmostEqual(d.total_vmt, 70.0)
        rows = {n: (a, b) for n, a, b in d.rows()}
        # 1 of 4 lane-km-hours moving, but 100% of the travel is in it.
        self.assertAlmostEqual(rows["60+"][0], 25.0)
        self.assertAlmostEqual(rows["60+"][1], 100.0)

    def test_lane_km_and_vmt_shares_can_disagree(self):
        # The whole reason both are reported: a little road carrying a lot of
        # traffic slowly is invisible in a lane-km share alone.
        d = rr.Dist(rr.V_BANDS)
        d.add("<10", lane_km_h=1.0, vmt=90.0)
        d.add("60+", lane_km_h=9.0, vmt=10.0)
        rows = {n: (a, b) for n, a, b in d.rows()}
        self.assertAlmostEqual(rows["<10"][0], 10.0)
        self.assertAlmostEqual(rows["<10"][1], 90.0)


class TestOpenMetrics(unittest.TestCase):
    def _write(self, intervals):
        fd, path = tempfile.mkstemp(suffix=".json")
        with os.fdopen(fd, "w") as f:
            json.dump({"ticks": 100, "dt": 0.1, "intervals": intervals,
                       "trips": [], "totals": {}}, f)
        self.addCleanup(os.unlink, path)
        return path

    def test_multiple_sets_are_refused_without_explicit_choice(self):
        p = self._write([interval("a", 0, 10, 1, 1, set_id="net"),
                         interval("a", 0, 10, 1, 1, set_id="corridor")])
        _, ivs, _ = rr.open_metrics(p, None)
        with self.assertRaises(SystemExit) as cm:
            list(ivs)
        self.assertIn("measurement set", str(cm.exception))

    def test_named_set_is_selected(self):
        p = self._write([interval("a", 0, 10, 1, 1, set_id="net"),
                         interval("b", 0, 10, 1, 1, set_id="corridor")])
        _, ivs, _ = rr.open_metrics(p, "corridor")
        self.assertEqual([iv["lane_id"] for iv in ivs], ["b"])

    def test_unknown_set_yields_nothing_and_is_reported_as_such(self):
        p = self._write([interval("a", 0, 10, 1, 1, set_id="net")])
        _, ivs, seen = rr.open_metrics(p, "nope")
        self.assertEqual(list(ivs), [])
        self.assertEqual(seen, set())

    def test_single_set_needs_no_choice(self):
        p = self._write([interval("a", 0, 10, 1, 1)])
        _, ivs, _ = rr.open_metrics(p, None)
        self.assertEqual(len(list(ivs)), 1)

    def test_trip_count_is_read_for_a_small_file(self):
        fd, path = tempfile.mkstemp(suffix=".json")
        with os.fdopen(fd, "w") as f:
            json.dump({"ticks": 10, "dt": 0.1, "totals": {},
                       "intervals": [interval("a", 0, 10, 1, 1)],
                       "trips": [{"vehicle_id": 1}, {"vehicle_id": 2}]}, f)
        self.addCleanup(os.unlink, path)
        head, _, _ = rr.open_metrics(path, None)
        self.assertEqual(head["n_trips"], 2)


class TestStreamIntervals(unittest.TestCase):
    """The streaming reader must agree with json.load, exactly.

    It exists because a 90-minute whole-network run is ~400 MB and 10M
    records; if it disagreed with the eager path the two would silently
    produce different reports for the same run depending on file size.
    """

    def _write(self, doc):
        fd, path = tempfile.mkstemp(suffix=".json")
        with os.fdopen(fd, "w") as f:
            json.dump(doc, f)
        self.addCleanup(os.unlink, path)
        return path

    def test_matches_json_load(self):
        ivs = [interval(f"lane{i}", i * 10, (i + 1) * 10, i * 1.5, i * 0.5)
               for i in range(250)]
        p = self._write({"ticks": 2500, "dt": 0.1, "trips": [],
                         "intervals": ivs, "totals": {"completed_trips": 7}})
        self.assertEqual(list(rr.stream_intervals(p)), ivs)

    def test_survives_a_tiny_read_chunk(self):
        # Forces records to straddle buffer boundaries constantly, which is
        # the only interesting failure mode in the reader.
        ivs = [interval(f"l{i}", i, i + 1, float(i), float(i)) for i in range(40)]
        p = self._write({"ticks": 40, "dt": 0.1, "trips": [],
                         "intervals": ivs, "totals": {}})
        self.assertEqual(list(rr.stream_intervals(p, chunk=7)), ivs)

    def test_empty_array(self):
        p = self._write({"ticks": 1, "dt": 0.1, "intervals": [], "totals": {}})
        self.assertEqual(list(rr.stream_intervals(p)), [])

    def test_load_head_finds_scalars_and_trailing_totals(self):
        ivs = [interval(f"l{i}", i, i + 1, 1.0, 1.0) for i in range(50)]
        p = self._write({"ticks": 4242, "dt": 0.1, "intervals": ivs,
                         "totals": {"completed_trips": 9, "active_at_horizon": 3}})
        head = rr.load_head(p)
        self.assertEqual(head["ticks"], 4242)
        self.assertEqual(head["dt"], 0.1)
        self.assertEqual(head["totals"]["completed_trips"], 9)


class TestEndToEnd(unittest.TestCase):
    """Drive the script the way a run does and read the JSON it writes."""

    def run_report(self, lanes, intervals, ticks=1000, extra=()):
        d = tempfile.mkdtemp()
        net = os.path.join(d, "net.json")
        met = os.path.join(d, "m.json")
        out = os.path.join(d, "out.json")
        with open(net, "w") as f:
            json.dump({"name": "t", "lanes": lanes}, f)
        with open(met, "w") as f:
            json.dump({"ticks": ticks, "dt": 0.1, "intervals": intervals,
                       "trips": [], "totals": {}}, f)
        r = subprocess.run(
            [sys.executable, SCRIPT, met, "--network", net, "--json", out,
             *extra], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        with open(out) as f:
            return json.load(f), r.stdout

    def test_partial_intervals_are_dropped_and_reported(self):
        lanes = [lane("a", 1000)]
        ivs = [interval("a", 0, 1000, 1000.0, 100.0),
               interval("a", 1000, 1500, 500.0, 50.0, partial=True)]
        out, text = self.run_report(lanes, ivs)
        self.assertEqual(out["window"]["dropped_partial"], 1)
        # Only the complete interval's travel is counted.
        self.assertAlmostEqual(out["totals"]["veh_km"], 1.0, places=6)
        self.assertIn("dropped 1 partial", text)

    def test_a_set_without_the_time_loss_group_is_refused_by_name(self):
        # engine/metrics.go declares TimeLossS as a pointer, nil when the
        # time_loss group is off, and the writer omits it. Subscripting it
        # raised a bare KeyError on a LEGAL metrics file. Delay is a headline
        # here and hotspots are ranked by it, so a silently-zero report would
        # read as "no delay" — refuse and name the group instead.
        d = tempfile.mkdtemp()
        net, met = os.path.join(d, "n.json"), os.path.join(d, "m.json")
        iv = interval("a", 0, 1000, 1000.0, 100.0)
        del iv["time_loss_s"]
        with open(net, "w") as f:
            json.dump({"lanes": [lane("a", 1000)]}, f)
        with open(met, "w") as f:
            json.dump({"ticks": 1000, "dt": 0.1, "trips": [], "totals": {},
                       "intervals": [iv]}, f)
        r = subprocess.run([sys.executable, SCRIPT, met, "--network", net],
                           capture_output=True, text=True)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("time_loss", r.stderr)
        self.assertNotIn("KeyError", r.stderr)
        self.assertNotIn("Traceback", r.stderr)

    def test_a_subset_set_is_not_reported_as_the_network(self):
        # ADR-0014 permits a set over a subset of the network. Lane-km comes
        # from the network file while travel comes only from lanes that
        # reported, so calling the result "network" divides a fraction of the
        # traffic by all of the road. Here one of two equal lanes is measured.
        lanes = [lane("seen", 1000), lane("unseen", 1000)]
        ivs = [interval("seen", 0, 36000, 1000.0, 3600.0)]
        out, text = self.run_report(lanes, ivs, ticks=36000)
        cov = out["coverage"]
        self.assertFalse(cov["covers_network"])
        self.assertEqual(cov["measured_lanes"], 1)
        self.assertEqual(cov["network_lanes"], 2)
        self.assertAlmostEqual(cov["measured_lane_km"], 1.0, places=6)
        self.assertAlmostEqual(cov["network_lane_km"], 2.0, places=6)
        self.assertIn("COVERAGE", text)
        # The distributions must not claim the network scope they do not have.
        self.assertIn("measured subset", text)
        # And the fixed denominator is the measured lane-km, so the curve's
        # density is 1 veh/km/lane (3600 veh-s over 3600 s over 1 km), not the
        # 0.5 that dividing by the whole network's 2 km would report.
        self.assertAlmostEqual(out["curve"][0]["k"], 1.0, places=6)

    def test_group_lane_km_is_narrowed_to_the_measured_lanes(self):
        # The first coverage fix corrected the NETWORK denominator but left
        # corridor and district lane-km at their whole-network values, so a
        # group's density was still diluted by lanes nobody watched. Two
        # corridor lanes, one measured: the corridor's lane-km must be the
        # 1 km measured, not the 2 km it spans.
        d = tempfile.mkdtemp()
        net = os.path.join(d, "n.json")
        cor = os.path.join(d, "c.json")
        met = os.path.join(d, "m.json")
        with open(net, "w") as f:
            json.dump({"lanes": [lane("seen", 1000), lane("unseen", 1000)]}, f)
        with open(cor, "w") as f:
            json.dump({"lanes": {"seen": "kennedy", "unseen": "kennedy"}}, f)
        with open(met, "w") as f:
            json.dump({"ticks": 36000, "dt": 0.1, "trips": [], "totals": {},
                       "intervals": [interval("seen", 0, 36000, 1000.0, 3600.0)]}, f)
        out = os.path.join(d, "o.json")
        r = subprocess.run(
            [sys.executable, SCRIPT, met, "--network", net,
             "--corridors", cor, "--json", out],
            capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        with open(out) as f:
            rep = json.load(f)
        row = rep["groups"]["corridors"]["kennedy"]
        self.assertAlmostEqual(row["lane_km"], 1.0, places=6)
        # 3600 veh-s over 3600 s over the 1 measured km = 1 veh/km/lane.
        self.assertAlmostEqual(row["k"], 1.0, places=6)
        self.assertAlmostEqual(rep["totals"]["corridor_lane_km"], 1.0, places=6)
        self.assertIn("narrowed to match", r.stdout)

    def test_totals_name_their_two_scopes_when_the_window_is_cut(self):
        # travel/Edie are summed over retained post-warmup cells; trips and
        # delay come from the kernel's run-total block and cover the whole
        # horizon. One "TOTALS (window)" heading over both combined two
        # populations silently.
        lanes = [lane("a", 1000)]
        ivs = [interval("a", 0, 18000, 500.0, 1800.0),
               interval("a", 18000, 36000, 500.0, 1800.0)]
        d = tempfile.mkdtemp()
        net, met = os.path.join(d, "n.json"), os.path.join(d, "m.json")
        out = os.path.join(d, "o.json")
        with open(net, "w") as f:
            json.dump({"lanes": lanes}, f)
        with open(met, "w") as f:
            json.dump({"ticks": 36000, "dt": 0.1, "trips": [],
                       "totals": {"completed_trips": 7,
                                  "active_at_horizon": 2,
                                  "mean_time_loss_s": 30.0,
                                  "total_time_loss_s": 210.0},
                       "intervals": ivs}, f)
        r = subprocess.run(
            [sys.executable, SCRIPT, met, "--network", net, "--json", out,
             "--warmup-tick", "18000"], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertNotIn("TOTALS (window)", r.stdout)
        self.assertIn("WHOLE RUN, not the window", r.stdout)
        with open(out) as f:
            rep = json.load(f)
        sc = rep["totals_scope"]
        self.assertFalse(sc["window_is_whole_run"])
        self.assertIn("veh_km", sc["window"])
        self.assertIn("completed_trips", sc["run"])
        self.assertIn("lane_km", sc["static"])

    def test_an_uncut_window_says_the_two_scopes_coincide(self):
        lanes = [lane("a", 1000)]
        ivs = [interval("a", 0, 36000, 1000.0, 3600.0)]
        out, text = self.run_report(lanes, ivs, ticks=36000)
        self.assertTrue(out["totals_scope"]["window_is_whole_run"])
        self.assertNotIn("WHOLE RUN, not the window", text)

    def test_a_full_set_still_reports_the_network_scope(self):
        lanes = [lane("a", 1000), lane("b", 1000)]
        ivs = [interval("a", 0, 36000, 1000.0, 3600.0),
               interval("b", 0, 36000, 1000.0, 3600.0)]
        out, text = self.run_report(lanes, ivs, ticks=36000)
        self.assertTrue(out["coverage"]["covers_network"])
        self.assertNotIn("COVERAGE", text)
        self.assertNotIn("measured subset", text)
        # Both lanes measured: denominator is the full 2 lane-km.
        self.assertAlmostEqual(out["curve"][0]["k"], 1.0, places=6)

    def test_all_partial_is_fatal_not_an_empty_report(self):
        d = tempfile.mkdtemp()
        net, met = os.path.join(d, "n.json"), os.path.join(d, "m.json")
        with open(net, "w") as f:
            json.dump({"lanes": [lane("a", 100)]}, f)
        with open(met, "w") as f:
            json.dump({"ticks": 10, "dt": 0.1, "trips": [], "totals": {},
                       "intervals": [interval("a", 0, 10, 1, 1, partial=True)]}, f)
        r = subprocess.run([sys.executable, SCRIPT, met, "--network", net],
                           capture_output=True, text=True)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("no complete interval", r.stderr)

    def test_density_is_per_km_of_that_lane(self):
        # Two lanes, same vehicle-seconds, one ten times longer. The short one
        # must land ten times denser. Averaging per-lane means would call them
        # equal, which is the error this report exists to make impossible.
        lanes = [lane("short", 100), lane("long", 1000)]
        dur_ticks = 36000                       # 3600 s at dt=0.1
        ivs = [interval("short", 0, dur_ticks, 1000.0, 3600.0),
               interval("long", 0, dur_ticks, 1000.0, 3600.0)]
        out, _ = self.run_report(lanes, ivs, ticks=dur_ticks)
        # 3600 veh-s over 3600 s = 1 vehicle present, on 0.1 km = 10 veh/km.
        # On 1.0 km that is 1 veh/km. 10/25 = 40% of critical, 1/25 = 4%.
        bands = out["density"]["network"]["bands"]
        self.assertGreater(bands["25-50%"]["pct_lane_km"], 0.0)
        self.assertGreater(bands["<25%"]["pct_lane_km"], 0.0)
        # The long lane is 10x the lane-km, so it dominates the <25% share.
        self.assertGreater(bands["<25%"]["pct_lane_km"],
                           bands["25-50%"]["pct_lane_km"])

    def test_empty_lane_is_a_bucket_not_a_zero_speed(self):
        lanes = [lane("moving", 1000), lane("empty", 1000)]
        ivs = [interval("moving", 0, 36000, 60000.0, 3600.0),
               interval("empty", 0, 36000, 0.0, 0.0)]
        out, _ = self.run_report(lanes, ivs, ticks=36000)
        self.assertAlmostEqual(out["speed"]["network"]["empty_pct"], 50.0,
                               places=6)
        # A zero-speed bucket would be the wrong answer for empty road.
        self.assertAlmostEqual(
            out["speed"]["network"]["bands"]["<10"]["pct_lane_km"], 0.0)

    def test_lane_km_denominator_is_the_network_not_the_occupied_part(self):
        # 'quiet' never reports at all. Its lane-km must still count, or an
        # emptying network reads as denser the more of it clears.
        lanes = [lane("busy", 1000), lane("quiet", 9000)]
        ivs = [interval("busy", 0, 36000, 60000.0, 3600.0)]
        out, _ = self.run_report(lanes, ivs, ticks=36000)
        self.assertAlmostEqual(out["totals"]["lane_km"], 10.0, places=6)

    def test_curve_separates_intervals(self):
        lanes = [lane("a", 1000)]
        ivs = [interval("a", 0, 18000, 30000.0, 1800.0),
               interval("a", 18000, 36000, 6000.0, 1800.0)]
        out, _ = self.run_report(lanes, ivs, ticks=36000)
        self.assertEqual(len(out["curve"]), 2)
        self.assertGreater(out["curve"][0]["speed"], out["curve"][1]["speed"])

    def test_hotspots_rank_by_delay(self):
        lanes = [lane("a", 100), lane("b", 100)]
        ivs = [interval("a", 0, 3600, 100.0, 100.0, loss=5.0),
               interval("b", 0, 3600, 100.0, 100.0, loss=500.0)]
        out, _ = self.run_report(lanes, ivs, ticks=3600)
        self.assertEqual(out["hotspots"][0]["lane"], "b")

    def test_measured_lane_absent_from_network_is_warned_not_silent(self):
        lanes = [lane("a", 1000)]
        ivs = [interval("a", 0, 3600, 100.0, 100.0),
               interval("ghost", 0, 3600, 100.0, 100.0)]
        _, text = self.run_report(lanes, ivs, ticks=3600)
        self.assertIn("absent from", text)


if __name__ == "__main__":
    unittest.main()
