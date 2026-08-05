#!/usr/bin/env python3
"""export_csv.py — flat CSV exports of the lineage dataset.

Mirrors the sci-fi-lineage project's export: the JSON dataset is the
source of truth, the CSVs are generated for spreadsheet / Gephi-style
consumers.

usage: export_csv.py
  reads  data/traffic_lineage.json
  writes data/nodes.csv, data/edges.csv
"""
import csv
import json
import os
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
LINEAGE_PATH = os.path.join(ROOT, "data", "traffic_lineage.json")
NODES_PATH = os.path.join(ROOT, "data", "nodes.csv")
EDGES_PATH = os.path.join(ROOT, "data", "edges.csv")


def main() -> None:
    with open(LINEAGE_PATH) as f:
        data = json.load(f)

    with open(NODES_PATH, "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["id", "label", "authors", "year", "venue",
                    "cited_by_count", "subfield", "tags", "note"])
        for n in data["nodes"]:
            w.writerow([n["id"], n["label"], "; ".join(n["authors"]),
                        n["year"], n["venue"] or "", n["cited_by_count"],
                        n["subfield"], ";".join(n["tags"]), n["note"]])

    with open(EDGES_PATH, "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["source", "target", "type", "on_main_path"])
        for e in data["edges"]:
            w.writerow([e["source"], e["target"], e["type"],
                        int(e["on_main_path"])])

    print(f"wrote {len(data['nodes'])} nodes -> {NODES_PATH}")
    print(f"wrote {len(data['edges'])} edges -> {EDGES_PATH}")


if __name__ == "__main__":
    sys.exit(main())
