#!/usr/bin/env python3
"""test_mkod.py — stdlib unittest coverage for mkod.py's reachability relation.

The relation decides which destinations a flow may be handed, so getting it
wrong is silent: the demand file still loads, the run still completes, and the
corridor speeds still look plausible. It just sends the wrong trips. These
tests pin it to the kernel's own relation (Engine.routeLatDepth,
engine/routing.go): successor edges cost 0 lane changes, lateral links cost 1,
no hop cap.

Run: python3 scripts/chicago/test_mkod.py
"""
import collections
import importlib.util
import types
import unittest
from pathlib import Path

_spec = importlib.util.spec_from_file_location(
    "mkod", Path(__file__).parent / "mkod.py")
_mod = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_mod)
can_reach = _mod.can_reach
lateral_links = _mod.lateral_links
spread = _mod.spread
build_profile = _mod.build_profile
ProfileSet = _mod.ProfileSet
emit_flow = _mod.emit_flow
zone_blend = _mod.zone_blend
peak_rate = _mod.peak_rate
total_veh = _mod.total_veh
AM_PROFILE = _mod.AM_PROFILE


def lane(lid, edge, idx, length=10.0, succ=()):
    return {"id": lid, "edge": edge, "edgeIndex": idx, "length": length,
            "successors": list(succ)}


def graph(lanes):
    """(lanes-by-id, preds, lat) from a lane list."""
    by_id = {l["id"]: l for l in lanes}
    preds = collections.defaultdict(list)
    for l in lanes:
        for s in l["successors"]:
            preds[s].append(l["id"])
    return by_id, preds, lateral_links(by_id)


class TestLateralLinks(unittest.TestCase):
    def test_consecutive_indices_link_mutually(self):
        lat = lateral_links({l["id"]: l for l in [
            lane("a", "e1", 0), lane("b", "e1", 1), lane("c", "e1", 2)]})
        self.assertEqual(sorted(lat["a"]), ["b"])
        self.assertEqual(sorted(lat["b"]), ["a", "c"])
        self.assertEqual(sorted(lat["c"]), ["b"])

    def test_index_gap_does_not_link(self):
        # A filtered lane sat between them, so they are not adjacent.
        lat = lateral_links({l["id"]: l for l in [
            lane("a", "e1", 0), lane("c", "e1", 2)]})
        self.assertEqual(lat["a"], [])
        self.assertEqual(lat["c"], [])

    def test_different_edges_never_link(self):
        lat = lateral_links({l["id"]: l for l in [
            lane("a", "e1", 0), lane("b", "e2", 1)]})
        self.assertEqual(lat["a"], [])

    def test_unequal_length_is_fatal(self):
        # The kernel refuses to load such a network (engine/netfile.go), so
        # emitting demand for it would only defer the error to run time.
        with self.assertRaises(SystemExit) as cm:
            lateral_links({l["id"]: l for l in [
                lane("a", "e1", 0, length=10.0),
                lane("b", "e1", 1, length=12.5)]})
        self.assertIn("differ in length", str(cm.exception))

    def test_lane_without_edge_is_skipped(self):
        lat = lateral_links({"x": {"id": "x", "length": 1.0}})
        self.assertEqual(lat["x"], [])


class TestCanReach(unittest.TestCase):
    def test_successor_chain(self):
        _, preds, lat = graph([lane("a", "e1", 0, succ=["b"]),
                               lane("b", "e2", 0, succ=["d"]),
                               lane("d", "e3", 0)])
        self.assertEqual(can_reach(preds, lat, "d"), {"a", "b", "d"})

    def test_lateral_hop_rescues_a_stranded_lane(self):
        # THE REGRESSION. `off` is the lane a boundary origin sits on; its only
        # successor leads off the network. Its lateral neighbour `main`
        # continues to the destination. Successor-only reachability says `off`
        # cannot reach `dest` — but the kernel steers it across and it can.
        # Five of chi-loop-urban's 26 freeway origins were in exactly this
        # state, sending 100% of their demand straight back out of the box.
        _, preds, lat = graph([lane("off", "e1", 0, succ=["gone"]),
                               lane("main", "e1", 1, succ=["dest"]),
                               lane("gone", "e2", 0),
                               lane("dest", "e3", 0)])
        R = can_reach(preds, lat, "dest")
        self.assertIn("main", R)
        self.assertIn("off", R)

    def test_multiple_hops_are_uncapped(self):
        # routeLatDepth has no hop cap; neither may this.
        lanes = [lane(f"l{i}", "e1", i) for i in range(5)]
        lanes[4]["successors"] = ["dest"]
        lanes.append(lane("dest", "e2", 0))
        _, preds, lat = graph(lanes)
        self.assertIn("l0", can_reach(preds, lat, "dest"))

    def test_unreachable_stays_unreachable(self):
        # A lateral link must not manufacture reachability across edges that
        # genuinely do not connect — otherwise the filter stops filtering.
        _, preds, lat = graph([lane("a", "e1", 0),
                               lane("dest", "e2", 0)])
        self.assertEqual(can_reach(preds, lat, "dest"), {"dest"})

    def test_cycle_terminates(self):
        _, preds, lat = graph([lane("a", "e1", 0, succ=["b"]),
                               lane("b", "e2", 0, succ=["a", "dest"]),
                               lane("dest", "e3", 0)])
        self.assertEqual(can_reach(preds, lat, "dest"), {"a", "b", "dest"})


class TestSpread(unittest.TestCase):
    """Injection-site spacing. Clustering here would recreate the very defect
    --ramp-share exists to remove: a corridor taking its volume at one point.
    """

    @staticmethod
    def pos(p):
        return p

    def test_takes_the_extremes_of_a_line(self):
        pts = [(x, 0.0) for x in range(11)]
        got = spread(pts, 3, self.pos)
        self.assertIn((0.0, 0.0), got)
        self.assertIn((10.0, 0.0), got)
        self.assertIn((5.0, 0.0), got)

    def test_colocated_candidates_do_not_consume_picks(self):
        # THE REGRESSION. A corridor carries parallel lanes at the same
        # longitudinal position; index spacing put two Dan Ryan injection
        # points 10 m apart across a 5.9 km corridor.
        pts = [(0.0, 0.0)] * 8 + [(1000.0, 0.0)] * 8 + [(2000.0, 0.0)] * 8
        got = spread(pts, 4, self.pos)
        self.assertEqual(sorted(set(got)),
                         [(0.0, 0.0), (1000.0, 0.0), (2000.0, 0.0)])

    def test_stops_when_every_position_is_taken(self):
        got = spread([(5.0, 5.0)] * 20, 6, self.pos)
        self.assertEqual(got, [(5.0, 5.0)])

    def test_separation_beats_index_spacing(self):
        # 20 candidates crammed at one end, 2 at the far end. Index spacing
        # would put 10 of 11 picks in the cluster.
        pts = [(i * 0.5, 0.0) for i in range(20)] + [(900.0, 0.0), (1000.0, 0.0)]
        got = spread(pts, 3, self.pos)
        self.assertIn((1000.0, 0.0), got)
        self.assertIn((900.0, 0.0), got)

    def test_preserves_input_order(self):
        pts = [(float(i), 0.0) for i in range(10)]
        got = spread(pts, 4, self.pos)
        self.assertEqual(got, sorted(got, key=pts.index))

    def test_degenerate_inputs(self):
        self.assertEqual(spread([], 5, self.pos), [])
        self.assertEqual(spread([(1.0, 1.0)], 0, self.pos), [])
        self.assertEqual(spread([(1.0, 1.0), (2.0, 2.0)], 1, self.pos),
                         [(1.0, 1.0)])

    def test_never_exceeds_n_and_never_duplicates(self):
        pts = [(float(i), float(i % 3)) for i in range(30)]
        for n in range(1, 12):
            got = spread(pts, n, self.pos)
            self.assertLessEqual(len(got), n, f"n={n}")
            self.assertEqual(len(got), len(set(got)), f"n={n}")

    def test_deterministic(self):
        pts = [(float(i % 7), float(i // 7)) for i in range(21)]
        self.assertEqual(spread(pts, 5, self.pos), spread(pts, 5, self.pos))


def profile_args(**kw):
    """The subset of the CLI namespace build_profile reads."""
    d = dict(flat_peak=False, slice_s=1800.0, drain_s=0.0, drain_level=0.0,
             horizon_s=0.0)
    d.update(kw)
    return types.SimpleNamespace(**d)


class TestBuildProfile(unittest.TestCase):
    """The demand program's shape and, crucially, its DURATION.

    A profile longer than the run that executes it is the failure this class
    exists to prevent: a 12,000-tick run against the 3-hour AM profile
    executes only the 0.45 opening ramp and reports it as the peak.
    """

    def test_default_is_the_real_three_hour_peak(self):
        prof = build_profile(profile_args())
        self.assertEqual(len(prof), len(AM_PROFILE))
        self.assertEqual(prof[0][0], 0.0)
        self.assertEqual(prof[-1][1], 10800.0)
        self.assertEqual([f for _, _, f in prof], AM_PROFILE)

    def test_slices_are_contiguous_and_ordered(self):
        prof = build_profile(profile_args(slice_s=600, drain_s=1800))
        for (_, end, _), (start, _, _) in zip(prof, prof[1:]):
            self.assertEqual(end, start)
        for start, end, _ in prof:
            self.assertLess(start, end)

    def test_slice_s_rescales_the_whole_shape(self):
        """Shortening the window must not change the SHAPE, only its span."""
        prof = build_profile(profile_args(slice_s=600))
        self.assertEqual([f for _, _, f in prof], AM_PROFILE)
        self.assertEqual(prof[-1][1], 600.0 * len(AM_PROFILE))

    def test_drain_appends_a_zero_tail_by_default(self):
        prof = build_profile(profile_args(slice_s=600, drain_s=1200))
        self.assertEqual(prof[-1], (3600, 4800, 0.0))

    def test_drain_level_is_a_fraction_of_peak_not_of_the_last_slice(self):
        """0.25 must mean a quarter of PEAK, not a quarter of the 0.60 taper.

        Anchoring the baseline to whatever the profile happened to end on
        would make the off-peak level move whenever the peak shape changed.
        """
        prof = build_profile(profile_args(slice_s=600, drain_s=1200,
                                          drain_level=0.25))
        self.assertAlmostEqual(prof[-1][2], 0.25 * max(AM_PROFILE))

    def test_flat_peak_spans_the_whole_horizon(self):
        """The regression: a hard-coded 0-1800 s slice silently stopped
        injecting partway through any run longer than 30 simulated minutes."""
        prof = build_profile(profile_args(flat_peak=True, horizon_s=5400))
        self.assertEqual(prof, [(0.0, 5400, max(AM_PROFILE))])

    def test_flat_peak_without_a_horizon_still_covers_the_full_profile(self):
        prof = build_profile(profile_args(flat_peak=True))
        self.assertEqual(prof[-1][1], len(AM_PROFILE) * 1800.0)

    def test_zero_rate_slices_are_dropped_not_emitted(self):
        """`veh_per_h: 0.0` is what a drain must NOT produce — the loader
        rejects a non-positive rate, and the intent is simply no arrivals."""
        out = []
        emit_flow(out, "f1", "laneA", 100.0,
                  build_profile(profile_args(slice_s=600, drain_s=1200)),
                  {}, {})
        # The taper's last slice ENDS at 3600; the drain slice would START
        # there. Only the latter must be absent.
        self.assertTrue(any("start_s: 3000, end_s: 3600" in ln for ln in out))
        self.assertFalse(any("start_s: 3600" in ln for ln in out))
        self.assertFalse(any("veh_per_h: 0.0" in ln for ln in out))

    def test_emitted_rates_scale_the_flow_by_the_fraction(self):
        out = []
        emit_flow(out, "f1", "laneA", 1000.0,
                  build_profile(profile_args(slice_s=600)), {}, {})
        rates = [ln.split("veh_per_h: ")[1].rstrip("}")
                 for ln in out if "veh_per_h" in ln]
        self.assertEqual(rates,
                         [f"{1000.0 * f:.1f}" for f in AM_PROFILE])


def pcfg(**kw):
    d = {
        "version": 1,
        "step_s": 600,
        "profiles": {"am": [0.5, 1.0, 0.5], "flat": [0.4, 0.4, 0.4]},
        "default": "am",
        "assign": [{"kind": "resident", "profile": "flat", "scale": 0.5}],
    }
    d.update(kw)
    return d


class TestProfileSet(unittest.TestCase):
    """The profile library, and above all its refusals.

    Every validation here exists because the alternative is silent: a
    misspelled corridor, an unknown profile name or a rule that never fires
    all produce a perfectly valid demand file that simply is not the one
    anybody authored.
    """

    def build(self, **kw):
        return ProfileSet(pcfg(**kw), "test.json")

    def test_rule_match_beats_default(self):
        ps = self.build()
        slices, scale = ps.resolve("resident", None, None)
        self.assertEqual([f for _, _, f in slices], [0.4, 0.4, 0.4])
        self.assertEqual(scale, 0.5)

    def test_default_catches_unmatched_flows(self):
        ps = self.build()
        slices, scale = ps.resolve("portal", "motorway", "kennedy")
        self.assertEqual([f for _, _, f in slices], [0.5, 1.0, 0.5])
        self.assertEqual(scale, 1.0)

    def test_first_matching_rule_wins(self):
        ps = self.build(assign=[
            {"kind": "portal", "profile": "flat"},
            {"kind": "portal", "profile": "am"},
        ])
        slices, _ = ps.resolve("portal", None, None)
        self.assertEqual([f for _, _, f in slices], [0.4, 0.4, 0.4])

    def test_all_match_keys_must_agree(self):
        ps = self.build(assign=[{"kind": "portal", "corridor": "kennedy",
                                 "profile": "flat"}])
        self.assertEqual(ps.resolve("portal", None, "kennedy")[0][0][2], 0.4)
        # right kind, wrong corridor -> falls through to the default
        self.assertEqual(ps.resolve("portal", None, "lsd")[0][0][2], 0.5)

    def test_spans_are_contiguous_from_zero(self):
        ps = self.build()
        self.assertEqual(ps.spans("am"),
                         [(0, 600, 0.5), (600, 1200, 1.0), (1200, 1800, 0.5)])

    def test_underscore_keys_are_comments(self):
        ps = self.build(assign=[{"_why": "note", "kind": "resident",
                                 "profile": "flat"}])
        self.assertEqual(len(ps.rules), 1)

    def test_unknown_key_is_fatal(self):
        with self.assertRaises(SystemExit) as e:
            self.build(assign=[{"kinds": "resident", "profile": "flat"}])
        self.assertIn("kinds", str(e.exception))

    def test_unknown_profile_reference_is_fatal(self):
        with self.assertRaises(SystemExit) as e:
            self.build(assign=[{"kind": "resident", "profile": "nope"}])
        self.assertIn("nope", str(e.exception))

    def test_unknown_default_is_fatal(self):
        with self.assertRaises(SystemExit):
            self.build(default="nope")

    def test_rule_without_match_key_is_fatal(self):
        """It would swallow every flow; `default` is how you say that."""
        with self.assertRaises(SystemExit):
            self.build(assign=[{"profile": "flat"}])

    def test_dead_rule_is_fatal(self):
        """A rule matching nothing reads exactly like a profile doing
        nothing — the --corridor-scale typo lesson."""
        ps = self.build(assign=[{"corridor": "eisenhauer", "profile": "flat"}])
        ps.resolve("portal", "motorway", "eisenhower")
        with self.assertRaises(SystemExit) as e:
            ps.check_all_rules_fired("test.json")
        self.assertIn("eisenhauer", str(e.exception))

    def test_fired_rule_passes_the_check(self):
        ps = self.build()
        ps.resolve("resident", None, None)
        ps.check_all_rules_fired("test.json")     # must not raise

    def test_bad_version_is_fatal(self):
        with self.assertRaises(SystemExit):
            self.build(version=2)

    def test_non_positive_step_is_fatal(self):
        with self.assertRaises(SystemExit):
            self.build(step_s=0)

    def test_negative_fraction_is_fatal(self):
        with self.assertRaises(SystemExit):
            self.build(profiles={"am": [0.5, -0.1]})

    def test_negative_scale_is_fatal(self):
        with self.assertRaises(SystemExit):
            self.build(assign=[{"kind": "resident", "profile": "flat",
                                "scale": -1}])

    def test_empty_profile_is_fatal(self):
        with self.assertRaises(SystemExit):
            self.build(profiles={"am": []})

    def test_no_rules_and_no_default_is_fatal(self):
        with self.assertRaises(SystemExit):
            self.build(assign=[], default=None)

    def test_span_is_the_longest_profile(self):
        ps = self.build(profiles={"short": [1.0], "long": [1.0, 1.0, 1.0]},
                        default="short", assign=[])
        self.assertEqual(ps.span_s, 1800.0)


# Floor areas standing in for a downtown lane, a near-north lane and two
# outlying ones. The CBD carries most of the area, which is the situation the
# pin exists to override.
W = {"d1": 60.0, "d2": 20.0, "n1": 15.0, "s1": 5.0}
Z = {"d1": "cbd", "d2": "cbd", "n1": "near-north", "s1": "south"}


def zshare(w, lane2zone, pins):
    """zone_blend, re-aggregated to per-district shares."""
    out = collections.defaultdict(float)
    for d, v in zone_blend(w, lane2zone, pins).items():
        out[lane2zone.get(d)] += v
    return dict(out)


class TestZoneBlend(unittest.TestCase):
    def test_no_pin_is_pure_floor_area(self):
        got = zone_blend(W, Z, {})
        self.assertAlmostEqual(got["d1"], 0.60)
        self.assertAlmostEqual(got["s1"], 0.05)

    def test_weights_always_sum_to_one(self):
        for pins in ({}, {"cbd": 0.3}, {"cbd": 0.9},
                     {"cbd": 0.5, "south": 0.4}, {"cbd": 1.0}):
            self.assertAlmostEqual(sum(zone_blend(W, Z, pins).values()), 1.0,
                                   places=9, msg=str(pins))

    def test_pin_sets_the_district_share_exactly(self):
        self.assertAlmostEqual(zshare(W, Z, {"cbd": 0.30})["cbd"], 0.30)
        self.assertAlmostEqual(zshare(W, Z, {"cbd": 0.90})["cbd"], 0.90)

    def test_pin_preserves_floor_area_split_inside_the_district(self):
        # 80% of the CBD's area is on d1; pinning the district must not
        # redistribute WITHIN it.
        got = zone_blend(W, Z, {"cbd": 0.50})
        self.assertAlmostEqual(got["d1"], 0.50 * 0.75)
        self.assertAlmostEqual(got["d2"], 0.50 * 0.25)

    def test_unpinned_districts_split_the_remainder_by_area(self):
        got = zshare(W, Z, {"cbd": 0.50})
        # near-north and south hold 15 and 5 of the 20 unpinned units.
        self.assertAlmostEqual(got["near-north"], 0.50 * 0.75)
        self.assertAlmostEqual(got["south"], 0.50 * 0.25)

    def test_several_pins_compose(self):
        got = zshare(W, Z, {"cbd": 0.40, "south": 0.40})
        self.assertAlmostEqual(got["cbd"], 0.40)
        self.assertAlmostEqual(got["south"], 0.40)
        self.assertAlmostEqual(got["near-north"], 0.20)

    def test_unreachable_pinned_district_does_not_lose_demand(self):
        # This origin cannot reach the CBD at all. The pinned 60% must be
        # spread over what it CAN reach, not silently dropped — dropping it
        # would shrink the flow and nothing downstream would notice.
        w = {"n1": 15.0, "s1": 5.0}
        got = zone_blend(w, Z, {"cbd": 0.60})
        self.assertAlmostEqual(sum(got.values()), 1.0)
        self.assertAlmostEqual(got["n1"], 0.75)

    def test_pin_of_one_leaves_nothing_elsewhere(self):
        got = zshare(W, Z, {"cbd": 1.0})
        self.assertAlmostEqual(got["cbd"], 1.0)
        self.assertNotIn("near-north", got)

    def test_only_the_pinned_district_reachable_absorbs_the_remainder(self):
        w = {"d1": 60.0, "d2": 20.0}
        got = zshare(w, Z, {"cbd": 0.40})
        self.assertAlmostEqual(got["cbd"], 1.0)

    def test_empty_weights_is_empty_not_a_crash(self):
        self.assertEqual(zone_blend({}, Z, {"cbd": 0.5}), {})

    def test_unzoned_destination_is_its_own_group(self):
        # A destination outside every district must not be silently folded
        # into a named one; it forms a None-keyed group that shares the
        # remainder like any other.
        w = dict(W, x1=20.0)
        got = zone_blend(w, Z, {"cbd": 0.50})
        self.assertAlmostEqual(sum(got.values()), 1.0)
        self.assertGreater(got["x1"], 0.0)


class TestPeakRate(unittest.TestCase):
    """What mkod reports as the demand level.

    The figure it used to print was a sum of per-flow BASE rates. Two separate
    errors lived in that: the base rate is not what emit_flow writes (it writes
    `rate * f`, and `f` tops out at 0.90 for freight), and a sum of per-flow
    peaks is not a peak at all once flows crest at different moments — which is
    the entire point of ADR-0028's shape library.
    """

    def test_concurrent_flows_add(self):
        got, at = peak_rate([(0, 600, 100.0), (0, 600, 50.0)])
        self.assertAlmostEqual(got, 150.0)
        self.assertEqual(at, 0)

    def test_flows_that_never_overlap_do_not_add(self):
        # THE bug, in miniature: summing per-flow peaks gives 150; no instant
        # in this program carries more than 100.
        got, at = peak_rate([(0, 600, 100.0), (600, 1200, 50.0)])
        self.assertAlmostEqual(got, 100.0)
        self.assertEqual(at, 0)

    def test_the_peak_can_be_in_the_middle(self):
        # Two shapes cresting at different slices, overlapping on the middle
        # one. The max is neither flow's own peak moment alone.
        segs = [(0, 600, 80.0), (600, 1200, 100.0),          # commute-ish
                (600, 1200, 40.0), (1200, 1800, 90.0)]       # freight-ish
        got, at = peak_rate(segs)
        self.assertAlmostEqual(got, 140.0)
        self.assertEqual(at, 600)

    def test_boundaries_are_half_open_so_neighbours_do_not_double_count(self):
        # emit_flow writes [start, end) slices; back-to-back slices from ONE
        # flow must never look like two concurrent flows at the shared second.
        got, _ = peak_rate([(0, 600, 100.0), (600, 1200, 100.0)])
        self.assertAlmostEqual(got, 100.0)

    def test_non_uniform_spans_need_no_common_grid(self):
        # --flat-peak and --drain-s produce unequal spans, and ADR-0028 allows
        # profiles of differing length. Overlap is 500-600.
        got, at = peak_rate([(0, 600, 70.0), (500, 2400, 30.0)])
        self.assertAlmostEqual(got, 100.0)
        self.assertEqual(at, 500)

    def test_an_empty_program_has_no_peak_rather_than_crashing(self):
        self.assertEqual(peak_rate([]), (0.0, 0.0))

    def test_the_reported_second_is_where_the_max_starts(self):
        _, at = peak_rate([(0, 600, 10.0), (1800, 2400, 99.0)])
        self.assertEqual(at, 1800)


class TestTotalVeh(unittest.TestCase):
    """The mass-balance basis: a count, not a rate.

    A through SHARE only means anything if both sides are integrated over the
    same span, and once flows run on different shapes they are not.
    """

    def test_an_hour_at_a_rate_is_that_many_vehicles(self):
        self.assertAlmostEqual(total_veh([(0, 3600, 250.0)]), 250.0)

    def test_a_ten_minute_slice_is_a_sixth_of_the_rate(self):
        self.assertAlmostEqual(total_veh([(0, 600, 600.0)]), 100.0)

    def test_slices_accumulate_across_the_whole_program(self):
        # The 9-slice x 600 s shape ADR-0028 uses, at a flat 100 veh/h: 90 min.
        segs = [(i * 600, (i + 1) * 600, 100.0) for i in range(9)]
        self.assertAlmostEqual(total_veh(segs), 150.0)

    def test_a_short_flat_peak_is_not_the_same_as_a_long_taper(self):
        # Same peak RATE, different vehicle counts — which is exactly why the
        # demand level cannot be read off a peak alone.
        flat = total_veh([(0, 600, 100.0)])
        taper = total_veh([(0, 600, 100.0), (600, 3000, 50.0)])
        self.assertAlmostEqual(flat, 16.6667, places=3)
        self.assertGreater(taper, flat)

    def test_an_empty_program_asks_for_no_vehicles(self):
        self.assertAlmostEqual(total_veh([]), 0.0)


if __name__ == "__main__":
    unittest.main()
