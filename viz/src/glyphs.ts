// glyphs.ts — rotated-rectangle vehicle symbols. Each BODY gets its own
// white rectangle drawn to an offscreen canvas at its true aspect ratio
// (engine dims via theme.ts, one shared pixel-per-metre scale) and
// registered with { sdf: true }: icon-image picks the body, icon-color
// tints, icon-rotate aims, and ONE icon-size zoom interpolation serves
// every layer. Bodies: the car, and the articulated truck's tractor and
// trailer (artic.ts infers the trailer pose; a single rigid truck image
// scaled per class was tried first and made trucks 2.4× WIDER as well as
// longer, spanning adjacent lanes — per-body images keep proportions
// honest).
//
// Rects are drawn VERTICALLY (long axis = image north) so icon-rotate can
// take the bearing from theme.ts:vehicleBearingDeg directly — at bearing 0
// (heading north) the unrotated image already reads as a vehicle pointing
// up-screen.
//
// Glyphs are screen-space by design: at corridor zooms (11–15) true-metric
// vehicle polygons render sub-pixel, so legibility wins over scale fidelity
// — class and heading are the data being encoded, not footprint. The
// proportions BETWEEN bodies stay honest (same px/m for every image).

import { TRACTOR_M, TRAILER_M, glyphByCls, type GlyphSpec } from "./theme.ts";

// Shared pixel-per-metre across body images keeps their proportions
// honest. The image is a binary-alpha rect — MapLibre computes the actual
// distance field from the alpha channel at render time; PAD just keeps
// that computed field from clipping at the image edge.
const PX_PER_M = 10;
const PAD = 8;

export const TRACTOR_IMAGE_ID = "veh-rect-tractor";
export const TRAILER_IMAGE_ID = "veh-rect-trailer";

export function glyphImageId(g: GlyphSpec): string {
  return `veh-rect-${g.name}`;
}

// makeRectImage draws a white lengthM × widthM rect (vertical, true aspect
// ratio at PX_PER_M) with a PAD px transparent margin. At icon-size 1
// the visible rect is lengthM × PX_PER_M px long.
export function makeRectImage(lengthM: number, widthM: number): ImageData {
  const w = Math.round(widthM * PX_PER_M) + 2 * PAD;
  const h = Math.round(lengthM * PX_PER_M) + 2 * PAD;
  const canvas = document.createElement("canvas");
  canvas.width = w;
  canvas.height = h;
  const ctx = canvas.getContext("2d");
  if (!ctx) throw new Error("glyphs: 2d canvas context unavailable");
  ctx.fillStyle = "#ffffff";
  ctx.fillRect(PAD, PAD, w - 2 * PAD, h - 2 * PAD);
  return ctx.getImageData(0, 0, w, h);
}

// Body images registered at map load: the car (whole vehicle), and the
// truck split into tractor + trailer at the theme.ts articulation split.
export function bodyImages(): Array<{ id: string; image: ImageData }> {
  const car = glyphByCls(0);
  const truck = glyphByCls(1);
  return [
    { id: glyphImageId(car), image: makeRectImage(car.lengthM, car.widthM) },
    { id: TRACTOR_IMAGE_ID, image: makeRectImage(TRACTOR_M, truck.widthM) },
    { id: TRAILER_IMAGE_ID, image: makeRectImage(TRAILER_M, truck.widthM) },
  ];
}

// One zoom curve for every layer: a car (50 px rect) is ~9 px long at
// zoom 14 — small enough that queued platoons stay separable, big enough
// to read the heading.
export const ICON_SIZE_STOPS: Array<[number, number]> = [
  [11, 0.086],
  [14, 0.18],
  [17, 0.43],
];
