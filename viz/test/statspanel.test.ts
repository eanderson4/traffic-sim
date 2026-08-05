// statspanel.test.ts — the pure half of the run-statistics panel: schema
// gating, the axis order (the report states it; the band names are not
// sortable strings), bar/axis scaling, group sorting, and the curve's
// minute-accurate layout. The last test parses the real sample document,
// so the panel's key names stay bound to what scripts/runreport.py --json
// actually writes.

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import {
  LEGACY_CRITICAL_K,
  bandOrder,
  barMax,
  coverageRow,
  criticalK,
  curveGeometry,
  distRows,
  f1,
  groupColumns,
  niceCeil,
  parseReport,
  scopeLabel,
  scopeOrder,
  sortGroupRows,
  summarySections,
  type CurvePoint,
  type Distribution,
  type GroupRow,
  type RunReport,
} from "../src/statspanel.ts";

// runreport.py's K_BANDS, as the report states them.
const K_ORDER = ["<25%", "25-50%", "50-75%", "75-100%", "100-150%", ">150%"];

const DIST: Distribution = {
  empty_pct: 29.4,
  band_order: K_ORDER,
  bands: {
    // Deliberately NOT in source order: band_order must win over key order.
    ">150%": { pct_lane_km: 1.7, pct_vmt: 5.8 },
    "<25%": { pct_lane_km: 58.1, pct_vmt: 54.6 },
    "100-150%": { pct_lane_km: 0.7, pct_vmt: 2.9 },
    "25-50%": { pct_lane_km: 7.4, pct_vmt: 28.0 },
    "50-75%": { pct_lane_km: 1.8, pct_vmt: 5.9 },
    "75-100%": { pct_lane_km: 0.9, pct_vmt: 2.9 },
  },
};

const REPORT: RunReport = {
  schema_version: 1,
  metrics: "/data/run/metrics.json",
  ticks: 12000,
  dt: 0.1,
  critical_k: 25,
  window: { begin_tick: 6000, end_tick: 12000, minutes: 10, dropped_partial: 3, skipped_warmup: 0 },
  totals: {
    lane_km: 2000,
    corridor_lane_km: 400,
    veh_km: 28822.4,
    veh_h: 1327.4,
    edie_kmh: 21.71,
    completed_trips: 1536,
    active_at_horizon: 10100,
    injected_trips: 20446,
    mean_time_loss_s: 2139.1,
    total_time_loss_s: 43736513.7,
  },
  density: { network: DIST },
  speed: { network: DIST },
  groups: {
    corridors: {
      lsd: { lane_km: 131.9, kmh: 26.3, k: 6.4, pct_lane_km_over_critical: 5.4, veh_h_lost: 84.1 },
      "dan-ryan": { lane_km: 75.8, kmh: 28.9, k: 6.2, pct_lane_km_over_critical: 7.3, veh_h_lost: 50.2 },
      "(arterial grid)": {
        lane_km: 1778.9,
        kmh: 16.9,
        k: 3.1,
        pct_lane_km_over_critical: 2.0,
        veh_h_lost: 610.1,
      },
    },
  },
  curve: [],
  hotspots: [],
};

test("parseReport: only schema_version 1 renders", () => {
  assert.equal(parseReport(REPORT).ok, true);
  const v2 = parseReport({ ...REPORT, schema_version: 2 });
  assert.equal(v2.ok, false);
  assert.match(v2.ok ? "" : v2.error, /schema_version 2/);
  assert.equal(parseReport({ ticks: 10 }).ok, false); // no schema_version at all
  assert.equal(parseReport(null).ok, false);
  assert.equal(parseReport([1, 2]).ok, false);
  assert.equal(parseReport("{}").ok, false);
  // v1 shape checked before any key binds
  const partial = parseReport({ schema_version: 1, window: {}, totals: {}, density: {} });
  assert.equal(partial.ok, false);
  assert.match(partial.ok ? "" : partial.error, /"speed" is missing/);
});

test("distRows: the REPORT's band_order is the axis, empty bucket first", () => {
  const rows = distRows(DIST);
  assert.deepEqual(
    rows.map((r) => r.band),
    ["empty", ...K_ORDER],
  );
  // The failure this pins: "100-150%" < "<25%" and ">150%" < "25-50%" as
  // strings, so any incidental sort reorders the axis. The fixture's key
  // order is wrong too — only band_order is right.
  assert.notDeepEqual([...K_ORDER].sort(), K_ORDER);
  assert.notDeepEqual(Object.keys(DIST.bands), K_ORDER);
  assert.equal(rows[0]!.empty, true);
  assert.equal(rows[0]!.vmt, null); // empty road has no share of travel
  assert.equal(rows[1]!.laneKm, 58.1);
  assert.equal(rows[1]!.vmt, 54.6);
});

test("bandOrder: key order is the fallback for pre-band_order documents", () => {
  const legacy: Distribution = { empty_pct: 1, bands: DIST.bands };
  assert.deepEqual(bandOrder(legacy), Object.keys(DIST.bands));
  assert.deepEqual(bandOrder(DIST), K_ORDER);
  assert.deepEqual(bandOrder({ empty_pct: 0, band_order: [], bands: { a: { pct_lane_km: 1, pct_vmt: 2 } } }), [
    "a",
  ]);
});

test("distRows: bands missing from band_order are appended, not dropped", () => {
  const d: Distribution = {
    empty_pct: 1,
    band_order: ["<25%"],
    bands: { "<25%": { pct_lane_km: 2, pct_vmt: 3 }, "200-300%": { pct_lane_km: 4, pct_vmt: 5 } },
  };
  assert.deepEqual(
    distRows(d).map((r) => r.band),
    ["empty", "<25%", "200-300%"],
  );
});

test("niceCeil / barMax: one readable scale shared by both series", () => {
  assert.equal(niceCeil(0), 1);
  assert.equal(niceCeil(-3), 1);
  assert.equal(niceCeil(0.7), 0.8);
  assert.equal(niceCeil(1.4), 1.5);
  assert.equal(niceCeil(2.4), 2.5);
  assert.equal(niceCeil(3), 3);
  assert.equal(niceCeil(58.1), 60);
  assert.equal(niceCeil(71.6), 80); // the sample's freeway peak
  assert.equal(niceCeil(140), 150);
  // Every scale halves cleanly — the axes carry a mid tick.
  for (const x of [0.7, 1.4, 2.4, 3, 58.1, 71.6, 140]) {
    const half = niceCeil(x) / 2;
    assert.equal(Math.round(half * 100) / 100, half, `${x} → ${niceCeil(x)}`);
  }
  // The scale covers the LARGER series — here VMT, not lane-km.
  assert.equal(barMax([{ band: "a", laneKm: 7.4, vmt: 28, empty: false }]), 30);
  assert.equal(barMax(distRows(DIST)), 60);
});

test("criticalK: the report states it; the constant is only a legacy fallback", () => {
  assert.equal(criticalK(REPORT), 25);
  assert.equal(criticalK({ ...REPORT, critical_k: 18 }), 18);
  const legacy = { ...REPORT };
  delete legacy.critical_k;
  assert.equal(criticalK(legacy), LEGACY_CRITICAL_K);
  assert.equal(criticalK({ ...REPORT, critical_k: 0 }), LEGACY_CRITICAL_K);
  // The %crit column names whatever the report cut its bands against.
  assert.match(groupColumns(18).find((c) => c.label === "%crit")!.title, /18 veh\/km\/lane/);
});

test("scopeOrder: runreport print order, extras kept", () => {
  assert.deepEqual(scopeOrder({ "arterial grid": DIST, network: DIST, corridors: DIST }), [
    "network",
    "corridors",
    "arterial grid",
  ]);
  assert.deepEqual(scopeOrder({ ramps: DIST, network: DIST }), ["network", "ramps"]);
});

test("sortGroupRows: defaults to veh-h lost descending; headers re-sort", () => {
  const rows = REPORT.groups!["corridors"]!;
  assert.deepEqual(
    sortGroupRows(rows, "veh_h_lost", true).map(([n]) => n),
    ["(arterial grid)", "lsd", "dan-ryan"],
  );
  assert.deepEqual(
    sortGroupRows(rows, "veh_h_lost", false).map(([n]) => n),
    ["dan-ryan", "lsd", "(arterial grid)"],
  );
  assert.deepEqual(
    sortGroupRows(rows, "name", false).map(([n]) => n),
    ["(arterial grid)", "dan-ryan", "lsd"],
  );
  assert.deepEqual(
    sortGroupRows(rows, "pct_lane_km_over_critical", true).map(([n]) => n),
    ["dan-ryan", "lsd", "(arterial grid)"],
  );
  // Ties fall back to name, so a re-sort is stable.
  const tied: Record<string, GroupRow> = {
    b: { lane_km: 1, kmh: 1, k: 1, pct_lane_km_over_critical: 1, veh_h_lost: 5 },
    a: { lane_km: 1, kmh: 1, k: 1, pct_lane_km_over_critical: 1, veh_h_lost: 5 },
  };
  assert.deepEqual(
    sortGroupRows(tied, "veh_h_lost", true).map(([n]) => n),
    ["a", "b"],
  );
});

test("summarySections: window as a share of the run; delay straight from totals", () => {
  const [run, totals] = summarySections(REPORT);
  assert.deepEqual(run!.rows[0], ["horizon", "12,000 ticks × 0.1 s = 20 min"]);
  assert.deepEqual(run!.rows[1], ["window", "ticks 6,000–12,000 = 10.0 min (50% of the run)"]);
  assert.deepEqual(run!.rows[2], ["dropped", "3 partial interval(s)"]);
  assert.match(run!.note ?? "", /ADR-0014/);
  assert.deepEqual(totals!.rows[0], ["network", "2,000.0 lane-km · 400.0 on corridors (20.0%)"]);
  assert.deepEqual(totals!.rows[1], ["travel", "28,822 veh-km over 1,327 veh-h"]);
  assert.deepEqual(totals!.rows[3], [
    "trips",
    "1,536 completed of 20,446 injected (7.5%) · 10,100 still driving at the horizon",
  ]);
  assert.deepEqual(totals!.rows[4], ["delay", "2,139 s mean loss per trip · 12,149 veh-h lost"]);
  // A pre-warmup skip is only reported when it happened.
  assert.equal(summarySections({ ...REPORT, window: { ...REPORT.window, skipped_warmup: 4 } })[0]!.rows.length, 4);
});

test("summarySections: no injected_trips (streamed metrics) prints no rate", () => {
  const streamed = summarySections({ ...REPORT, totals: { ...REPORT.totals, injected_trips: null } });
  assert.deepEqual(streamed[1]!.rows[3], ["trips", "1,536 completed · 10,100 still driving at the horizon"]);
  assert.equal(
    streamed[1]!.rows.some(([, v]) => v.includes("injected")),
    false,
  );
  // A document with no delay figures gets NO delay row: the sum over
  // groups it could be derived from is not a substitute for the report
  // saying it.
  const noDelay = { ...REPORT.totals };
  delete noDelay.mean_time_loss_s;
  delete noDelay.total_time_loss_s;
  assert.equal(
    summarySections({ ...REPORT, totals: noDelay })[1]!.rows.some(([k]) => k === "delay"),
    false,
  );
});

test("curveGeometry: x is real minutes at interval midpoints", () => {
  const curve: CurvePoint[] = [
    {
      begin_min: 10,
      end_min: 15,
      speed: 25.7,
      k: 3.1,
      fw_speed: 39.5,
      fw_k: 5.0,
      pct_over_critical: 1.9,
      pct_fwy_over_critical: 3.5,
    },
    {
      begin_min: 15,
      end_min: 20,
      speed: 18.7,
      k: 4.1,
      fw_speed: 27.6,
      fw_k: 6.2,
      pct_over_critical: 3.0,
      pct_fwy_over_critical: 5.6,
    },
  ];
  const g = curveGeometry(curve, 400, 124)!;
  assert.ok(g);
  assert.equal(g.xMin, 10);
  assert.equal(g.xMax, 20);
  assert.equal(g.vMax, 40); // niceCeil(39.5)
  assert.equal(g.pMax, 6); // niceCeil(5.6)
  const plotW = 400 - g.pad.l - g.pad.r;
  const plotH = 124 - g.pad.t - g.pad.b;
  const net = g.series.find((s) => s.key === "speed")!;
  // Midpoints 12.5 and 17.5 over the 10–20 domain land at 1/4 and 3/4.
  assert.equal(net.pts[0]!.x, g.pad.l + 0.25 * plotW);
  assert.equal(net.pts[1]!.x, g.pad.l + 0.75 * plotW);
  assert.ok(Math.abs(net.pts[0]!.y - (g.pad.t + (1 - 25.7 / 40) * plotH)) < 1e-9);
  // Slower later = lower on the chart (y grows downward).
  assert.ok(net.pts[1]!.y > net.pts[0]!.y);
  // The %-over-critical series reads the RIGHT axis, not the speed one.
  const over = g.series.find((s) => s.key === "pct_fwy_over_critical")!;
  assert.ok(Math.abs(over.pts[1]!.y - (g.pad.t + (1 - 5.6 / 6) * plotH)) < 1e-9);
  assert.match(net.path, /^M [\d.]+ [\d.]+ L [\d.]+ [\d.]+$/);
  assert.deepEqual(
    g.xTicks.map((t) => t.label),
    ["10", "15", "20"],
  );
  assert.deepEqual(
    g.yLeft.map((t) => t.label),
    ["40", "20", "0"],
  );
  assert.equal(curveGeometry([], 400, 124), null);
});

test("curveGeometry: a 90-minute run spans 0–90 and labels at most 8 x ticks", () => {
  // 18 five-minute intervals, network speed falling monotonically — the
  // shape of the real sample.
  const curve: CurvePoint[] = Array.from({ length: 18 }, (_, i) => ({
    begin_min: i * 5,
    end_min: (i + 1) * 5,
    speed: 43 - 2.2 * i,
    k: 1 + i,
    fw_speed: 72 - 3.4 * i,
    fw_k: 1 + i,
    pct_over_critical: 0,
    pct_fwy_over_critical: i < 12 ? 0.4 * i : 5 - 0.3 * (i - 12),
  }));
  const g = curveGeometry(curve)!;
  assert.equal(g.xMin, 0);
  assert.equal(g.xMax, 90); // the whole run, not the last interval's start
  assert.equal(g.series[0]!.pts.length, 18);
  assert.ok(g.xTicks.length <= 8, `${g.xTicks.length} ticks`);
  assert.equal(g.xTicks[0]!.label, "0");
  assert.equal(g.xTicks[g.xTicks.length - 1]!.label, "90");
  // The first sample sits half an interval in, the last half an interval
  // from the end — nothing is clipped to the axis.
  const plotW = g.w - g.pad.l - g.pad.r;
  assert.ok(g.series[0]!.pts[0]!.x > g.pad.l);
  assert.ok(g.series[0]!.pts[17]!.x < g.pad.l + plotW);
  // Samples stay far enough apart to read as separate dots (r = 1.6).
  const gap = g.series[0]!.pts[1]!.x - g.series[0]!.pts[0]!.x;
  assert.ok(gap > 6, `dot spacing ${gap}`);
  // A monotonic decline stays monotonic on screen.
  const ys = g.series[0]!.pts.map((p) => p.y);
  assert.deepEqual(ys, [...ys].sort((a, b) => a - b));
});

test("the shipped sample parses and every block binds", () => {
  const raw = readFileSync(new URL("../public/sample-runreport.json", import.meta.url), "utf8");
  const res = parseReport(JSON.parse(raw));
  assert.equal(res.ok, true);
  if (!res.ok) return;
  const r = res.report;
  assert.equal(criticalK(r), 25);
  assert.deepEqual(scopeOrder(r.density), ["network", "corridors", "arterial grid"]);
  const kd = r.density["network"]!;
  const vd = r.speed["corridors"]!;
  // The axis order comes from the document, and it is NOT what sorting the
  // same names would give.
  assert.deepEqual(bandOrder(kd), K_ORDER);
  assert.notDeepEqual([...bandOrder(vd)].sort(), [...bandOrder(vd)]);
  assert.deepEqual(
    distRows(kd).map((row) => row.band),
    ["empty", ...bandOrder(kd)],
  );
  assert.deepEqual(
    distRows(vd).map((row) => row.band),
    ["empty", ...bandOrder(vd)],
  );
  assert.deepEqual(Object.keys(r.groups ?? {}), ["corridors", "districts"]);
  assert.ok(sortGroupRows(r.groups!["districts"]!, "veh_h_lost", true).length > 0);
  // 90-minute run: 18 intervals, spanning the full horizon.
  const g = curveGeometry(r.curve ?? [])!;
  assert.equal(g.series[0]!.pts.length, 18);
  assert.equal(g.xMin, 0);
  assert.equal(g.xMax, 90);
  const hot = (r.hotspots ?? [])[0]!;
  assert.equal(typeof hot.lane, "string");
  assert.equal(typeof hot.x, "number");
  // Hotspots arrive already ordered by delay — the table keeps that order.
  const losses = (r.hotspots ?? []).map((h) => h.veh_h_lost);
  assert.deepEqual(losses, [...losses].sort((a, b) => b - a));
  // Totals carry their own delay and their own denominator now.
  assert.equal(typeof r.totals.total_time_loss_s, "number");
  assert.equal(typeof r.totals.mean_time_loss_s, "number");
  const trips = summarySections(r)[1]!.rows.find(([k]) => k === "trips")!;
  // BOTH shapes are legal and the panel must render each correctly.
  // injected_trips is null on a STREAMED metrics file — runreport declines to
  // make a second full pass over `trips` just to get a denominator, on the
  // grounds that a wrong denominator is worse than an absent one. This
  // assertion used to require the populated shape only, so it failed on any
  // report built from a source over the 200 MB streaming threshold (the
  // shipped sample's own source is 396 MB) — it was pinning the small-input
  // case as if it were the only one.
  if (r.totals.injected_trips != null) {
    assert.match(trips[1], /completed of [\d,]+ injected \(\d+\.\d%\)/);
  } else {
    assert.match(trips[1], /^[\d,]+ completed · [\d,]+ still driving/);
    // The percentage must be OMITTED, not rendered from a null denominator.
    assert.ok(!/injected/.test(trips[1]), trips[1]);
    assert.ok(!/NaN|Infinity|undefined|null/.test(trips[1]), trips[1]);
  }
});

test("totals rows name their scope only when the two spans differ", () => {
  const raw = readFileSync(new URL("../public/sample-runreport.json", import.meta.url), "utf8");
  const doc = JSON.parse(raw);

  // Window cut away from the horizon: travel and trips describe different
  // populations, so each row must say which one it is.
  doc.totals_scope = { window_is_whole_run: false };
  const cut = parseReport(doc);
  assert.equal(cut.ok, true);
  if (!cut.ok) return;
  const cutRows = new Map(summarySections(cut.report)[1]!.rows);
  assert.match(cutRows.get("travel")!, /· window$/);
  assert.match(cutRows.get("Edie speed")!, /· window$/);
  assert.match(cutRows.get("delay")!, /· whole run$/);
  assert.equal(summarySections(cut.report)[1]!.title, "totals");

  // Uncut: the tags would be noise, so they are dropped.
  doc.totals_scope = { window_is_whole_run: true };
  const full = parseReport(doc);
  assert.equal(full.ok, true);
  if (!full.ok) return;
  const fullRows = new Map(summarySections(full.report)[1]!.rows);
  assert.ok(!/window|whole run/.test(fullRows.get("travel")!), fullRows.get("travel")!);
  assert.ok(!/window|whole run/.test(fullRows.get("delay")!), fullRows.get("delay")!);

  // Absent (older document): say nothing rather than guess a scope.
  delete doc.totals_scope;
  const old = parseReport(doc);
  assert.equal(old.ok, true);
  if (!old.ok) return;
  const oldRows = new Map(summarySections(old.report)[1]!.rows);
  assert.ok(!/window|whole run/.test(oldRows.get("travel")!), oldRows.get("travel")!);
});

test("a null injected count omits the rate rather than inventing one", () => {
  // The streamed-file path, pinned directly so it does not depend on which
  // sample happens to ship. Dividing by a null denominator would print
  // "NaN%" or a confident "0.0%", and both read as a measurement.
  const raw = readFileSync(new URL("../public/sample-runreport.json", import.meta.url), "utf8");
  const doc = JSON.parse(raw);
  doc.totals.injected_trips = null;
  doc.totals.completed_trips = 6397;
  doc.totals.active_at_horizon = 14049;
  const res = parseReport(doc);
  assert.equal(res.ok, true);
  if (!res.ok) return;
  const row = summarySections(res.report)[1]!.rows.find(([k]) => k === "trips")!;
  assert.equal(row[1], "6,397 completed · 14,049 still driving at the horizon");

  doc.totals.injected_trips = 20446;
  const res2 = parseReport(doc);
  assert.equal(res2.ok, true);
  if (!res2.ok) return;
  const row2 = summarySections(res2.report)[1]!.rows.find(([k]) => k === "trips")!;
  assert.match(row2[1], /6,397 completed of 20,446 injected \(31\.3%\)/);
});

test("a subset measurement set is never labelled the network", () => {
  // The producer was fixed to DERIVE and report coverage; the panel then
  // ignored the field and went on calling a subset "network", recreating the
  // ADR-0030 defect on the consumer side. Both halves have to agree.
  const partial = { covers_network: false, measured_lanes: 500, network_lanes: 55555, measured_lane_km: 40, network_lane_km: 2203.8 };
  assert.equal(scopeLabel("network", partial), "measured subset");
  // Only the network scope is affected — corridors are corridors either way.
  assert.equal(scopeLabel("corridors", partial), "corridors");
  assert.equal(scopeLabel("arterial grid", partial), "arterial grid");

  // A full set, and an older report with no coverage block, both stay
  // "network": absent knowledge must not become a warning.
  assert.equal(scopeLabel("network", { covers_network: true }), "network");
  assert.equal(scopeLabel("network", undefined), "network");
  assert.equal(scopeLabel("network", {}), "network");
});

test("coverageRow describes a partial set and stays silent otherwise", () => {
  const row = coverageRow({
    covers_network: false,
    measured_lanes: 500,
    network_lanes: 55555,
    measured_lane_km: 40,
    network_lane_km: 2000,
  })!;
  assert.equal(row[0], "COVERAGE");
  assert.match(row[1], /500 of 55,555 lanes/);
  assert.match(row[1], /40\.0 of 2,000\.0 lane-km \(2\.0%\)/);
  assert.match(row[1], /absent, not empty/);

  assert.equal(coverageRow({ covers_network: true }), null);
  assert.equal(coverageRow(undefined), null);
  assert.equal(coverageRow({}), null);
});

test("a subset report shows measured lane-km and a coverage row first", () => {
  const raw = readFileSync(new URL("../public/sample-runreport.json", import.meta.url), "utf8");
  const doc = JSON.parse(raw);
  const netKm = doc.totals.lane_km;
  doc.coverage = {
    covers_network: false,
    measured_lanes: 100,
    network_lanes: 55555,
    measured_lane_km: netKm / 4,
    network_lane_km: netKm,
  };
  const res = parseReport(doc);
  assert.equal(res.ok, true);
  if (!res.ok) return;
  const section = summarySections(res.report)[1]!;
  assert.equal(section.rows[0]![0], "COVERAGE");
  const rows = new Map(section.rows);
  // The lane-km row is the MEASURED lane-km, and is not called "network".
  assert.ok(!rows.has("network"));
  assert.match(rows.get("measured")!, new RegExp(`^${f1(netKm / 4).replace(".", "\\.")} lane-km`));
  // Corridor share divides by the measured base, not the whole network.
  const corr = doc.totals.corridor_lane_km;
  if (corr > 0) {
    const expected = ((100 * corr) / (netKm / 4)).toFixed(1);
    assert.match(rows.get("measured")!, new RegExp(`\\(${expected}%\\)`));
  }
});
