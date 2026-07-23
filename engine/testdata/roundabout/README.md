# Fixture: roundabout — Urachplatz, Stuttgart (Ostfildern)

A single-lane roundabout with four arms, cropped from OSM. Roundabouts carry
no `junction=roundabout`-specific control in the compiled file: netconvert
splits the ring into one priority junction per entry node, and netimport
(ADR-0010) compiles the circulatory connections as `major` and the entry
connections as `minor` (yield). The fixture exercises the priority-junction
right-of-way model in its roundabout form: circulating traffic owns the ring,
entries yield.

Pinned test input: `network.json` (+ `import-report.json`). Regenerate only
with the recipe below; the JSON, not the recipe output of the day, is what
tests run against. OSM data © OpenStreetMap contributors, ODbL — tiny extract
(~20 ways), attribution noted here.

## What the fixture contains

- Ring ways `24938992`, `201841693`, `599183014` (`junction=roundabout`,
  name "Urachplatz", tertiary, maxspeed 40, ref L 1016).
- Four arms, truncated at ~140 m path distance from the ring so they dangle
  (open boundary): Werfmershalde (W, way 599182989+3998057), Spittlerstraße
  (N, 9663293), Haußmannstraße (E, 99370300+9703875+599183001+599183025),
  Urachstraße (S, 368911279+368911280+23075483).
- Compiled: 58 lanes (25 normal, 33 internal), 64 connections, 5 origins,
  5 exits, no signals. Ring-entry junctions (e.g. 75748238 — the netconvert
  junction cluster joining the W/N entries — 271013442, 271013450,
  271013455, 497224071, 76479362) carry major/minor rows with
  FoesCross/FoesMerge conflict wiring.
- Routing note: the engine follows first successors, and the ring's
  first-successor chain is a closed loop (n201841693_0_0 → … →
  n24938992_1_0 → …). Vehicles entering the ring therefore circulate
  indefinitely; only the W-origin first-successor path drains (W → N exit
  via minor internal i75748238_8_0). The behavior test accounts for this.

## Recipe (run 2026-07-23)

1. Overpass extract, ~350 m bbox around the ring (48.78588, 9.19745):

   ```
   [out:xml][timeout:90];(way["highway"](48.7843,9.1951,48.7873,9.1998);>;);out body;
   ```

   (overpass-api.de; alternates overpass.kumi.systems,
   overpass.private.coffee) → `map.osm`. Verified `junction=roundabout` on
   ways 24938992/201841693.

2. Crop to the fixture (two small Python steps, scripts below):
   - whitelist the ring + four arm way chains, drop every other way (the
     Stuttgart block mesh is so tight that any larger keep closes loops —
     side streets reconnect the arms inside the crop);
   - BFS from the ring nodes along the way graph, keep nodes within 140 m
     of path distance, truncate ways at the horizon so every arm dangles
     mid-segment (way splits get numeric ids `orig*10+i` — netconvert
     requires numeric way ids).

3. netconvert (tools/sumo-venv, netconvert 1.27.1 from the eclipse-sumo
   PyPI package):

   ```
   netconvert --osm-files cut.osm -o net.net.xml --no-turnarounds
   ```

   `--no-turnarounds` is ESSENTIAL: netconvert otherwise builds U-turn
   connections at the truncated arm ends, closing every arm into itself
   (the import then has 0 origins/0 exits and nothing can enter or leave).

4. netimport:

   ```
   cd engine && go run ./cmd/netimport -in /tmp/rb-net.net.xml \
     -out ./testdata/roundabout/network.json \
     -bbox "48.78435,9.19512,48.78741,9.19978" -name roundabout \
     -source "netimport (netconvert 1.27.1 .net.xml, eclipse-sumo PyPI)" \
     -report ./testdata/roundabout/import-report.json
   ```

### Crop scripts (Python 3, stdlib only)

Whitelist step:

```python
import xml.etree.ElementTree as ET
KEEP={'24938992','201841693','599183014',            # ring (Urachplatz)
      '599182989','3998057',                          # W arm Werfmershalde
      '99370300','9703875','599183001','599183025',   # E arm Haußmannstraße
      '9663293',                                      # N arm Spittlerstraße
      '368911279','368911280','23075483'}             # S arm Urachstraße
tree=ET.parse('map.osm'); root=tree.getroot()
for w in root.findall('way'):
    if w.get('id') not in KEEP: root.remove(w)
tree.write('arms.osm',xml_declaration=True,encoding='UTF-8')
```

Truncate step (`ring_crop.py arms.osm cut.osm 140 24938992 201841693 599183014`):

```python
import sys, math, heapq, xml.etree.ElementTree as ET
src, dst, R = sys.argv[1], sys.argv[2], float(sys.argv[3])
RING = set(sys.argv[4:])
NONMOTOR = {'footway','path','steps','cycleway','track','pedestrian',
            'construction','corridor','elevator','platform'}
tree = ET.parse(src); root = tree.getroot()
nodes = {n.get('id'): (float(n.get('lat')), float(n.get('lon'))) for n in root.findall('node')}
def seg(a, b):
    la, lo = nodes[a]; lb, lob = nodes[b]
    return math.hypot((la-lb)*111000, (lo-lob)*73000*math.cos(math.radians((la+lb)/2)))
ways = {}
for w in root.findall('way'):
    tags = {t.get('k'): t.get('v') for t in w.findall('tag')}
    if tags.get('highway') in NONMOTOR: continue
    ways[w.get('id')] = (w, [nd.get('ref') for nd in w.findall('nd')])
ringnodes = set()
for rid in RING: ringnodes |= set(ways[rid][1])
dist = {n: 0.0 for n in ringnodes}
adj = {}
for wid, (w, refs) in ways.items():
    for a, b in zip(refs, refs[1:]):
        d = seg(a, b)
        adj.setdefault(a, []).append((b, d)); adj.setdefault(b, []).append((a, d))
pq = [(0.0, n) for n in ringnodes]; heapq.heapify(pq)
while pq:
    d, n = heapq.heappop(pq)
    if d > dist.get(n, math.inf): continue
    for m, w_ in adj.get(n, []):
        nd = d + w_
        if nd < dist.get(m, math.inf):
            dist[m] = nd; heapq.heappush(pq, (nd, m))
keep = {n for n, d in dist.items() if d <= R}
for wid, (w, refs) in list(ways.items()):
    runs, cur = [], []
    for r_ in refs:
        if r_ in keep: cur.append(r_)
        else: runs.append(cur); cur = []
    runs.append(cur)
    runs = [r_ for r_ in runs if len(r_) >= 2]
    if not runs: root.remove(w); continue
    for nd in w.findall('nd'): w.remove(nd)
    for i, run in enumerate(runs):
        if i == 0:
            for r_ in run: ET.SubElement(w, 'nd', {'ref': r_})
        else:
            nw = ET.SubElement(root, 'way', {'id': str(int(wid)*10+i)})
            for r_ in run: ET.SubElement(nw, 'nd', {'ref': r_})
            for tg in w.findall('tag'):
                ET.SubElement(nw, 'tag', {'k': tg.get('k'), 'v': tg.get('v')})
for w in root.findall('way'):
    tags = {t.get('k'): t.get('v') for t in w.findall('tag')}
    if tags.get('highway') in NONMOTOR: root.remove(w)
tree.write(dst, xml_declaration=True, encoding='UTF-8')
```
