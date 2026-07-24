#!/usr/bin/env python3
"""test_overpass2osm.py — stdlib unittest coverage for overpass2osm.py:
(1) the new node-tags Overpass shape (real nodes emitted verbatim with
their tags, ways reference nd ids in order, no geometry synthesis),
(2) the old geometry-only shape converts byte-for-byte as before (golden
file captured from the pre-change script), (3) XML attribute escaping in
node tag values.

Run: python3 scripts/test_overpass2osm.py
"""
import json
import unittest
from pathlib import Path

from overpass2osm import convert

TESTDATA = Path(__file__).parent / "testdata"


def new_shape() -> dict:
    # Mirrors the re-pull query: ways with node refs and no geometry, then
    # real node elements — plain way nodes untagged, stop/signal nodes
    # tagged (highway=stop + direction).
    return {
        "elements": [
            {
                "type": "way",
                "id": 5001,
                "nodes": [910000001, 910000002, 910000003],
                "tags": {"highway": "residential", "name": "Main St"},
            },
            {
                "type": "way",
                "id": 5002,
                "nodes": [910000004, 910000002],
                "tags": {"highway": "primary"},
            },
            {"type": "node", "id": 910000001, "lat": 33.7490, "lon": -84.3880},
            {
                "type": "node",
                "id": 910000002,
                "lat": 33.7495,
                "lon": -84.3870,
                "tags": {"highway": "stop", "direction": "forward"},
            },
            {"type": "node", "id": 910000003, "lat": 33.7500, "lon": -84.3860},
            {
                "type": "node",
                "id": 910000004,
                "lat": 33.7492,
                "lon": -84.3865,
                "tags": {"highway": "traffic_signals"},
            },
            # Unreferenced tagged node (the query pulls stop/signal nodes
            # directly): still emitted — netconvert drops what it can't use.
            {
                "type": "node",
                "id": 910000009,
                "lat": 33.7600,
                "lon": -84.3900,
                "tags": {"highway": "give_way"},
            },
        ]
    }


class NewShapeTest(unittest.TestCase):
    def setUp(self) -> None:
        self.xml = convert(new_shape())

    def test_node_tags_survive(self) -> None:
        self.assertIn('<node id="910000002" lat="33.7495" lon="-84.387">\n', self.xml)
        self.assertIn('<tag k="highway" v="stop"/>', self.xml)
        self.assertIn('<tag k="direction" v="forward"/>', self.xml)
        self.assertIn('<tag k="highway" v="traffic_signals"/>', self.xml)

    def test_untagged_nodes_self_close(self) -> None:
        self.assertIn('<node id="910000001" lat="33.749" lon="-84.388"/>\n', self.xml)

    def test_way_refs_in_order(self) -> None:
        way = (
            '  <way id="5001">\n'
            '    <nd ref="910000001"/>\n'
            '    <nd ref="910000002"/>\n'
            '    <nd ref="910000003"/>\n'
        )
        self.assertIn(way, self.xml)

    def test_no_geometry_synthesis(self) -> None:
        # Every node id appears exactly once (no synthesized duplicates of
        # the real nodes), and unreferenced nodes are still emitted.
        for nid in (910000001, 910000002, 910000003, 910000004, 910000009):
            self.assertEqual(self.xml.count(f'<node id="{nid}"'), 1, nid)

    def test_nodes_before_ways(self) -> None:
        self.assertLess(self.xml.index("<node "), self.xml.index("<way "))


class OldShapeGoldenTest(unittest.TestCase):
    def test_byte_for_byte(self) -> None:
        data = json.loads((TESTDATA / "old-shape.json").read_text())
        golden = (TESTDATA / "old-shape-golden.osm").read_text()
        self.assertEqual(convert(data), golden)


class EscapingTest(unittest.TestCase):
    def test_node_tag_values_escaped(self) -> None:
        data = {
            "elements": [
                {
                    "type": "node",
                    "id": 1,
                    "lat": 1.0,
                    "lon": 2.0,
                    "tags": {"note": 'A & "B" <C>'},
                }
            ]
        }
        xml = convert(data)
        self.assertIn('<tag k="note" v="A &amp; &quot;B&quot; &lt;C&gt;"/>', xml)
        self.assertNotIn('"B"', xml.replace("&quot;", ""))  # no raw quotes left


if __name__ == "__main__":
    unittest.main()
