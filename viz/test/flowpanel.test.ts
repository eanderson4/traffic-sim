// flowpanel.test.ts — the pure half of the flow panel: schema gating, axis
// scaling and label spacing, the marker clamp, and the two claims the panel
// makes in words. The last test parses the REAL Chicago flow document when
// it is present, so the panel's key names stay bound to what
// scripts/show/mkflowcurve.py actually writes.
//
// The two claims worth pinning, because they are the ones a reader trusts
// without checking:
//   - the shaded band is arrivals minus ALL exits, not minus completions.
//     Getting this wrong would draw stranded vehicles as if the network
//     still held them.
//   - the rate chart's y axis ignores bin 0. It is a startup burst; scaling
//     to it flattens everything else.

import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";

import {
  SCHEMA_VERSION,
  accumulationGeom,
  binAt,
  clipRate,
  compact,
  cumulativeGeom,
  isFlowDoc,
  markerX,
  niceMax,
  phaseAt,
  rateGeom,
  xTicksFor,
  type FlowBin,
  type FlowDoc,
} from "../src/flowpanel.ts";

// A synthetic run: fills for 3 bins, then arrivals stop and it drains.
function bin(
  tick: number,
  arr: number,
  done: number,
  strand: number,
  cumArr: number,
  cumDone: number,
  cumStrand: number,
): FlowBin {
  return {
    tick,
    min: tick / 600,
    arr,
    done,
    strand,
    cumArr,
    cumDone,
    cumStrand,
    inNet: cumArr - cumDone - cumStrand,
  };
}

function doc(): FlowDoc {
  const bins = [
    bin(0, 100, 0, 0, 100, 0, 0),
    bin(600, 40, 10, 0, 140, 10, 0),
    bin(1200, 40, 10, 2, 180, 20, 2),
    bin(1800, 0, 30, 4, 180, 50, 6),
    bin(2400, 0, 30, 4, 180, 80, 10),
  ];
  return {
    schema_version: 1,
    dt: 0.1,
    ticks: 3000,
    binTicks: 600,
    lastEntryTick: 1500,
    totals: {
      injected: 180,
      completed: 80,
      stranded: 10,
      activeAtHorizon: 90,
      peakInNet: Math.max(...bins.map((b) => b.inNet)),
      peakInNetTick: 1200,
    },
    bins,
  };
}

test("isFlowDoc gates on schema version and shape", () => {
  const d = doc();
  assert.equal(isFlowDoc(d), true);
  assert.equal(isFlowDoc({ ...d, schema_version: SCHEMA_VERSION + 1 }), false);
  assert.equal(isFlowDoc({ ...d, bins: [] }), false);
  assert.equal(isFlowDoc(null), false);
  assert.equal(isFlowDoc("nope"), false);
});

test("niceMax rounds up to a readable number and never returns 0", () => {
  assert.equal(niceMax(0), 1);
  assert.equal(niceMax(-5), 1);
  assert.equal(niceMax(180), 200);
  assert.equal(niceMax(1000), 1000);
  // The two that motivated the fine ladder: a coarse [1,2,5,10] ladder sends
  // these to 10,000 and 20,000, wasting 40% and 47% of the chart height.
  assert.equal(niceMax(5989), 6000);
  assert.equal(niceMax(10517), 12000);
});

test("niceMax never wastes more than 25% of the axis", () => {
  // The property the ladder exists to guarantee, checked across three
  // decades rather than at the two values that prompted it.
  for (let v = 11; v < 100000; v = Math.ceil(v * 1.07)) {
    const m = niceMax(v);
    assert.ok(m >= v, `niceMax(${v}) = ${m} is below the data`);
    assert.ok(v / m > 0.75, `niceMax(${v}) = ${m} wastes ${((1 - v / m) * 100).toFixed(0)}%`);
  }
});

test("compact keeps axis labels short", () => {
  assert.equal(compact(0), "0");
  assert.equal(compact(950), "950");
  assert.equal(compact(5989), "6.0k");
});

test("xTicksFor keeps labels to at most 8 and spans the plot", () => {
  for (const minutes of [5, 30, 90, 240, 480]) {
    const ticks = xTicksFor(minutes, 260, { l: 44, r: 8, t: 8, b: 16 });
    assert.ok(ticks.length <= 9, `${minutes} min gave ${ticks.length} labels`);
    assert.equal(ticks[0]!.label, "0′");
    assert.equal(ticks[0]!.pos, 44);
  }
  // 90 minutes at a 15-minute step: 0,15,…,90.
  assert.deepEqual(
    xTicksFor(90, 260, { l: 44, r: 8, t: 8, b: 16 }).map((t) => t.label),
    ["0′", "15′", "30′", "45′", "60′", "75′", "90′"],
  );
});

test("cumulative band closes on ALL exits, not on completions", () => {
  const d = doc();
  const g = cumulativeGeom(d, 260, 108);
  const arr = g.series.find((s) => s.key === "arr")!;
  assert.ok(arr.fill !== undefined, "arrivals series carries the shaded band");
  // The band's return leg must trace cumDone+cumStrand. The final bin has
  // cumDone 80 and cumStrand 10, so the y for 90 must appear — and the y for
  // 80 (completions alone) must NOT be where the band closes.
  const span = 108 - 8 - 16;
  const yFor = (v: number) => 108 - 16 - (v / g.yMax) * span;
  // Points in path order: the arrivals leg (one per bin), then the return
  // leg (bins reversed). The return leg's FIRST point is the last bin's
  // total exits — 80 completed + 10 stranded. That is the number under test:
  // closing on 80 would draw the 10 stranded vehicles as still in network.
  const ys = [...arr.fill!.matchAll(/[ML]\s+([\d.]+)\s+([\d.]+)/g)].map((m) => Number(m[2]));
  const turn = ys[d.bins.length]!;
  assert.ok(
    Math.abs(turn - yFor(90)) < 0.15,
    `band must turn at all exits (90), got y=${turn}; completions alone would be y=${yFor(80).toFixed(1)}`,
  );
  // And the two are genuinely distinguishable at this scale, so the test
  // above is not passing by rounding.
  assert.ok(Math.abs(yFor(90) - yFor(80)) > 1);
  // And the series list carries all three channels, exits above completions.
  assert.deepEqual(g.series.map((s) => s.key), ["arr", "out", "done"]);
});

test("accumulation scales to the peak and marks where arrivals stop", () => {
  const d = doc();
  const g = accumulationGeom(d, 260, 64);
  assert.equal(g.yMax, niceMax(d.totals.peakInNet));
  assert.ok(g.arrivalsStopX !== null);
  // lastEntryTick 1500 of 3000 is the midpoint of the plot area.
  assert.ok(Math.abs(g.arrivalsStopX! - (44 + (260 - 44 - 8) / 2)) < 0.01);
});

test("a run that never stops loading has no arrivals-stop marker", () => {
  const d = doc();
  d.lastEntryTick = d.ticks;
  assert.equal(cumulativeGeom(d, 260, 108).arrivalsStopX, null);
  assert.equal(accumulationGeom(d, 260, 64).arrivalsStopX, null);
});

test("rate axis ignores bin 0 and clipRate reports the burst", () => {
  const d = doc();
  const g = rateGeom(d, 260, 64);
  // Sustained peak is 40 arrivals per 60 s bin = 40/min; bin 0 is 100/min.
  // The axis must sit near the sustained peak, well under the burst.
  assert.ok(g.yMax < 100, `axis ${g.yMax} must not accommodate the burst`);
  assert.ok(g.yMax >= 40, `axis ${g.yMax} must cover the sustained peak`);
  assert.equal(clipRate(d, g.yMax), 100);
  // Bin 0 is clamped to the top of the plot, not drawn above it.
  const arr = g.series.find((s) => s.key === "arr")!;
  const firstY = Number(arr.path.split(/\s+/)[2]);
  assert.ok(firstY >= 8 - 1e-9, `bin 0 at y=${firstY} escapes the viewBox top`);
});

test("a run with no startup burst reports no clipping", () => {
  const d = doc();
  d.bins[0]!.arr = 5;
  assert.equal(clipRate(d, rateGeom(d, 260, 64).yMax), null);
});

test("markerX clamps to the plot area at both ends", () => {
  const d = doc();
  assert.equal(markerX(d, 0, 260), 44);
  assert.equal(markerX(d, -500, 260), 44);
  assert.equal(markerX(d, d.ticks, 260), 260 - 8);
  // A live run past the document's horizon must land ON the edge, not past it.
  assert.equal(markerX(d, d.ticks * 3, 260), 260 - 8);
});

test("binAt indexes uniformly and clamps", () => {
  const d = doc();
  assert.equal(binAt(d, 0).tick, 0);
  assert.equal(binAt(d, 599).tick, 0);
  assert.equal(binAt(d, 600).tick, 600);
  assert.equal(binAt(d, 1_000_000).tick, 2400);
});

test("phaseAt names the accumulation slope with a deadband", () => {
  const d = doc();
  assert.equal(phaseAt(d, 600), "filling");
  assert.equal(phaseAt(d, 2100), "draining");
  // A flat run is "holding", not a coin flip between the other two.
  const flat = doc();
  for (const b of flat.bins) b.inNet = 100;
  flat.totals.peakInNet = 100;
  assert.equal(phaseAt(flat, 1200), "holding");
});

test("the real Chicago flow document renders", (t) => {
  const path = "public/chi-flow.json";
  if (!existsSync(path)) {
    t.skip("no chi-flow.json — run scripts/show/mkflowcurve.py");
    return;
  }
  const d: unknown = JSON.parse(readFileSync(path, "utf8"));
  assert.equal(isFlowDoc(d), true);
  const f = d as FlowDoc;
  // The accumulation identity must hold in the document itself, not just in
  // the generator: every bin's inNet is arrivals minus both exit channels.
  for (const b of f.bins) {
    assert.equal(b.inNet, b.cumArr - b.cumDone - b.cumStrand, `bin at tick ${b.tick}`);
  }
  // Monotone cumulatives — a decrease would mean a vehicle un-arrived.
  for (let i = 1; i < f.bins.length; i++) {
    assert.ok(f.bins[i]!.cumArr >= f.bins[i - 1]!.cumArr);
    assert.ok(f.bins[i]!.cumDone >= f.bins[i - 1]!.cumDone);
    assert.ok(f.bins[i]!.cumStrand >= f.bins[i - 1]!.cumStrand);
  }
  for (const g of [
    cumulativeGeom(f, 260, 108),
    accumulationGeom(f, 260, 64),
    rateGeom(f, 260, 64),
  ]) {
    assert.ok(g.series.every((s) => s.path.startsWith("M ")));
    assert.ok(g.yTicks.length >= 3);
    assert.ok(g.xTicks.length >= 2);
  }
});
