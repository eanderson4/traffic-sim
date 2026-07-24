#!/usr/bin/env python3
"""test_osm_stop_nodes.py — stdlib unittest coverage for osm-stop-nodes.py:
direction-aware walk resolution (forward/backward/absent/both), stop node
tagged at the junction -> allway_stop, all-arms-stopped -> allway_stop,
partial -> priority_stop, file-boundary walk skips, arm dedupe.

Run: python3 scripts/test_osm_stop_nodes.py
"""
import importlib.util
import unittest
from pathlib import Path

# The script name carries a hyphen (osm-stop-nodes.py), so load it by path.
_spec = importlib.util.spec_from_file_location(
    "osm_stop_nodes", Path(__file__).parent / "osm-stop-nodes.py"
)
_mod = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_mod)
resolve = _mod.resolve
render = _mod.render


def way(wid: int, refs: list[int]) -> dict:
    return {"type": "way", "id": wid, "nodes": refs}


def node(nid: int, tags: dict | None = None) -> dict:
    el = {"type": "node", "id": nid, "lat": 1.0, "lon": 2.0}
    if tags:
        el["tags"] = tags
    return el


def stop(nid: int, **tags: str) -> dict:
    return node(nid, {"highway": "stop", **tags})


class DirectionTest(unittest.TestCase):
    def test_forward_walks_to_higher_index(self) -> None:
        # Stop at 2 on way A [1,2,3]; junction 3 shared with way B.
        data = {"elements": [way(10, [1, 2, 3]), way(11, [3, 4]), stop(2, direction="forward")]}
        self.assertEqual(resolve(data), {3: "priority_stop"})

    def test_backward_walks_to_lower_index(self) -> None:
        # Same way, backward: junction 1 shared with way C.
        data = {"elements": [way(10, [1, 2, 3]), way(11, [1, 5]), stop(2, direction="backward")]}
        self.assertEqual(resolve(data), {1: "priority_stop"})

    def test_absent_direction_defaults_to_forward(self) -> None:
        data = {"elements": [way(10, [1, 2, 3]), way(11, [3, 4]), stop(2)]}
        self.assertEqual(resolve(data), {3: "priority_stop"})

    def test_both_marks_both_ends(self) -> None:
        data = {
            "elements": [
                way(10, [1, 2, 3]),
                way(11, [1, 5]),
                way(12, [3, 6]),
                stop(2, direction="both"),
            ]
        }
        self.assertEqual(resolve(data), {1: "priority_stop", 3: "priority_stop"})


class AggregationTest(unittest.TestCase):
    def test_stop_tagged_at_junction_is_priority(self) -> None:
        # Bare stop node shared by two ways: stop control, but only
        # stop=all means all-way (KB convention).
        data = {"elements": [way(10, [1, 2]), way(11, [2, 3]), stop(2)]}
        self.assertEqual(resolve(data), {2: "priority_stop"})

    def test_stop_all_tagged_at_junction_is_allway(self) -> None:
        data = {"elements": [way(10, [1, 2]), way(11, [2, 3]), stop(2, stop="all")]}
        self.assertEqual(resolve(data), {2: "allway_stop"})

    def test_all_arms_stopped_is_allway(self) -> None:
        # Junction 3 shared by ways A and B; both approaches carry a stop.
        data = {
            "elements": [
                way(10, [7, 8, 3]),
                way(11, [9, 4, 3]),
                stop(8, direction="forward"),
                stop(4, direction="forward"),
            ]
        }
        self.assertEqual(resolve(data), {3: "allway_stop"})

    def test_partial_arms_is_priority(self) -> None:
        # Junction 3 has two incident ways, only one's approach stopped.
        data = {"elements": [way(10, [7, 8, 3]), way(11, [9, 3]), stop(8)]}
        self.assertEqual(resolve(data), {3: "priority_stop"})

    def test_stop_all_tag_is_allway(self) -> None:
        # OSM stop=all on the walk's target, tagged mid-way.
        data = {"elements": [way(10, [7, 8, 3]), way(11, [9, 3]), stop(8, stop="all")]}
        self.assertEqual(resolve(data), {3: "allway_stop"})


class EdgeCaseTest(unittest.TestCase):
    def test_way_split_is_not_a_junction(self) -> None:
        # One named road split into two ways at node 3 (same class + name);
        # the walk must pass through 3 to the real junction at 4.
        named = {"highway": "residential", "name": "Main St"}
        data = {
            "elements": [
                way(10, [1, 2, 3]),
                way(11, [3, 4]),
                way(12, [9, 4]),
                stop(2, direction="forward"),
            ]
        }
        data["elements"][0]["tags"] = named
        data["elements"][1]["tags"] = named
        self.assertEqual(resolve(data), {4: "priority_stop"})

    def test_stop_tagged_at_way_split_walks_on(self) -> None:
        # A stop node sitting ON a split point (same road, same name) is
        # not "tagged at the intersection" — no allway at the split.
        named = {"highway": "residential", "name": "Main St"}
        data = {
            "elements": [
                way(10, [1, 2]),
                way(11, [2, 3]),
                way(12, [9, 3]),
                stop(2),
            ]
        }
        data["elements"][0]["tags"] = named
        data["elements"][1]["tags"] = named
        self.assertEqual(resolve(data), {3: "priority_stop"})

    def test_t_junction_directed_arms(self) -> None:
        # Through way A with junction 3 interior (2 arms), way B ending at
        # 3 (1 arm): stops on one A-approach and on B = 2 of 3 arms, so
        # priority_stop, NOT allway_stop.
        data = {
            "elements": [
                way(10, [7, 8, 3, 20]),
                way(11, [9, 4, 3]),
                stop(8, direction="forward"),
                stop(4, direction="forward"),
            ]
        }
        self.assertEqual(resolve(data), {3: "priority_stop"})

    def test_all_directed_arms_stopped_is_allway(self) -> None:
        # Same T-junction; both A approaches + B stopped = 3 of 3 arms.
        data = {
            "elements": [
                way(10, [7, 8, 3, 21, 20]),
                way(11, [9, 4, 3]),
                stop(8, direction="forward"),
                stop(21, direction="backward"),
                stop(4, direction="forward"),
            ]
        }
        self.assertEqual(resolve(data), {3: "allway_stop"})

    def test_oneway_counts_one_arm(self) -> None:
        # Two oneway ways ending at 3: one arm each; both stopped -> allway.
        data = {
            "elements": [
                way(10, [7, 8, 3]),
                way(11, [9, 4, 3]),
                stop(8),
                stop(4),
            ]
        }
        data["elements"][0]["tags"] = {"highway": "residential", "oneway": "yes"}
        data["elements"][1]["tags"] = {"highway": "residential", "oneway": "yes"}
        self.assertEqual(resolve(data), {3: "allway_stop"})

    def test_oneway_starting_at_junction_adds_no_arm(self) -> None:
        # Way 11 is oneway STARTING at 3 (outbound only): 0 arms. Way 10
        # ends at 3 with its approach stopped -> 1 of 1 arms -> allway.
        data = {
            "elements": [
                way(10, [7, 8, 3]),
                way(11, [3, 9, 4]),
                stop(8),
            ]
        }
        data["elements"][1]["tags"] = {"highway": "residential", "oneway": "yes"}
        self.assertEqual(resolve(data), {3: "allway_stop"})

    def test_both_direction_with_split_hop(self) -> None:
        # Regression: the split hop rebinds refs; it must not leak into the
        # second walk of direction=both. Ways 10=[1,2,3], 11=[3,4,5] split
        # at 3 (same name), junctions at 1 and 5, stop at 2 both ways.
        named = {"highway": "residential", "name": "Main St"}
        data = {
            "elements": [
                way(10, [1, 2, 3]),
                way(11, [3, 4, 5]),
                way(12, [1, 8]),
                way(13, [5, 9]),
                stop(2, direction="both"),
            ]
        }
        data["elements"][0]["tags"] = named
        data["elements"][1]["tags"] = named
        self.assertEqual(resolve(data), {1: "priority_stop", 5: "priority_stop"})

    def test_signalized_junction_never_overridden(self) -> None:
        # The stop's walk targets junction 3, but 3 is signalized: no
        # override (pass 2 would replace the light with a stop junction).
        data = {
            "elements": [
                way(10, [7, 8, 3]),
                way(11, [9, 3]),
                stop(8, direction="forward"),
                node(3, {"highway": "traffic_signals"}),
            ]
        }
        self.assertEqual(resolve(data), {})

    def test_stop_tag_at_signal_junction_ignored(self) -> None:
        data = {
            "elements": [
                way(10, [1, 2]),
                way(11, [2, 3]),
                node(2, {"highway": "traffic_signals"}),
                stop(2),
            ]
        }
        self.assertEqual(resolve(data), {})

    def test_directionless_on_oneway_minus_one_walks_backward(self) -> None:
        # No direction tag, but the way is oneway=-1: travel is against
        # node order, so the stop resolves to the LOWER-index junction.
        data = {
            "elements": [
                way(10, [1, 2, 3]),
                way(11, [1, 5]),
                way(12, [3, 6]),
                stop(2),
            ]
        }
        data["elements"][0]["tags"] = {"highway": "residential", "oneway": "-1"}
        self.assertEqual(resolve(data), {1: "priority_stop"})

    def test_boundary_walk_skips(self) -> None:
        # Way ends at 3 without meeting another included way: nothing to
        # control, no junction marked.
        data = {"elements": [way(10, [1, 2, 3]), stop(2, direction="forward")]}
        self.assertEqual(resolve(data), {})

    def test_duplicate_stops_on_same_arm_dedupe(self) -> None:
        data = {
            "elements": [
                way(10, [7, 8, 9, 3]),
                way(11, [3, 4]),
                stop(8, direction="forward"),
                stop(9, direction="forward"),
            ]
        }
        self.assertEqual(resolve(data), {3: "priority_stop"})
        self.assertEqual(render(resolve(data)).count("<node "), 1)

    def test_render_sorted_and_escaped(self) -> None:
        xml = render({20: "priority_stop", 3: "allway_stop", 100: "priority_stop"})
        self.assertLess(xml.index('id="3"'), xml.index('id="20"'))
        self.assertLess(xml.index('id="20"'), xml.index('id="100"'))
        self.assertIn('<node id="3" type="allway_stop"/>', xml)


if __name__ == "__main__":
    unittest.main()
