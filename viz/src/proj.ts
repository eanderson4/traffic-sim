// proj.ts — projection from the network's local metric frame to WGS84
// [lng, lat]. TSSF v1 frames and the exported network GeoJSON both carry
// local-frame coordinates (engine/natsio/frame.go, engine/geojson.go); the
// frame descriptor (projection + netOffset, copied from the network
// provenance, network-format-v1.md) tells the client how to project:
//
//   absolute UTM = local − netOffset      (SUMO netOffset convention)
//   WGS84        = inverse UTM(zone) of the absolute coordinate
//
// Only "+proj=utm +zone=N" on WGS84 is supported — every netimport network
// today (netconvert --proj.utm). The inverse transverse Mercator is the
// standard Snyder/USGS series, good to well under a millimetre here.

export interface LocalFrame {
  projection: string;
  netOffset: [number, number];
}

export type Projector = (x: number, y: number) => [number, number];

const WGS84_A = 6378137.0;
const WGS84_E2 = 6.69437999014e-3;
const UTM_K0 = 0.9996;
const UTM_FALSE_EASTING = 500000.0;

function parseUtmZone(projection: string): number {
  const tokens = projection.trim().split(/\s+/);
  let isUtm = false;
  let zone: number | null = null;
  for (const tok of tokens) {
    if (tok === "+proj=utm") isUtm = true;
    if (tok.startsWith("+zone=")) zone = Number(tok.slice("+zone=".length));
    if (tok === "+south") {
      throw new Error(`proj: southern-hemisphere UTM not supported: ${projection}`);
    }
  }
  if (!isUtm || zone === null || !Number.isInteger(zone) || zone < 1 || zone > 60) {
    throw new Error(`proj: only "+proj=utm +zone=N (1..60)" is supported, got: ${projection}`);
  }
  return zone;
}

// makeProjector compiles a frame descriptor into a local(x,y) → [lng, lat]
// function (lng/lat in degrees). Throws on unsupported projections.
export function makeProjector(frame: LocalFrame): Projector {
  const zone = parseUtmZone(frame.projection);
  const lon0 = ((zone - 1) * 6 - 180 + 3) * (Math.PI / 180); // central meridian
  const [offX, offY] = frame.netOffset;
  const e2 = WGS84_E2;
  const ep2 = e2 / (1 - e2);
  const e1 = (1 - Math.sqrt(1 - e2)) / (1 + Math.sqrt(1 - e2));
  const meridional =
    WGS84_A * (1 - e2 / 4 - (3 * e2 * e2) / 64 - (5 * e2 * e2 * e2) / 256);

  return (x: number, y: number): [number, number] => {
    const easting = x - offX;
    const northing = y - offY;
    const dE = easting - UTM_FALSE_EASTING;
    const mu = northing / UTM_K0 / meridional;
    // Footpoint latitude (Snyder series).
    const phi1 =
      mu +
      ((3 * e1) / 2 - (27 * e1 ** 3) / 32) * Math.sin(2 * mu) +
      ((21 * e1 ** 2) / 16 - (55 * e1 ** 4) / 32) * Math.sin(4 * mu) +
      ((151 * e1 ** 3) / 96) * Math.sin(6 * mu) +
      ((1097 * e1 ** 4) / 512) * Math.sin(8 * mu);
    const sin1 = Math.sin(phi1);
    const cos1 = Math.cos(phi1);
    const tan1 = Math.tan(phi1);
    const n1 = WGS84_A / Math.sqrt(1 - e2 * sin1 * sin1);
    const r1 = (WGS84_A * (1 - e2)) / (1 - e2 * sin1 * sin1) ** 1.5;
    const t1 = tan1 * tan1;
    const c1 = ep2 * cos1 * cos1;
    const d = dE / (n1 * UTM_K0);
    const lat =
      phi1 -
      ((n1 * tan1) / r1) *
        (d ** 2 / 2 -
          ((5 + 3 * t1 + 10 * c1 - 4 * c1 * c1 - 9 * ep2) * d ** 4) / 24 +
          ((61 + 90 * t1 + 298 * c1 + 45 * t1 * t1 - 252 * ep2 - 3 * c1 * c1) * d ** 6) / 720);
    const lon =
      lon0 +
      (d -
        ((1 + 2 * t1 + c1) * d ** 3) / 6 +
        ((5 - 2 * c1 + 28 * t1 - 3 * c1 * c1 + 8 * ep2 + 24 * t1 * t1) * d ** 5) / 120) /
        cos1;
    const deg = 180 / Math.PI;
    return [lon * deg, lat * deg];
  };
}
