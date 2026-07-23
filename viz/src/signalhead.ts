// signalhead.ts — the signal-head sprite: a vertical three-lens traffic
// light (housing + three DIM lenses) drawn to an offscreen canvas and
// registered as a plain RGBA image. The LIT lens is not baked into the
// sprite — three circle layers (one per lens position, fixed
// circle-translate, opacity gated by the feature-state "sig" color) paint
// the active light on top, because icon-image expressions cannot read
// feature-state (MapLibre style spec: icon-image takes zoom + feature
// only). The exported geometry constants let the lens layers track the
// sprite exactly at any icon-size.
//
// Lens order mirrors a real head: red top, amber middle, green bottom.

// Image geometry (pixels at icon-size 1).
export const SIGNAL_HEAD_IMAGE_ID = "signal-head";
const W = 14; // housing width
const H = 34; // housing height
const PAD = 3; // transparent margin so the lens glow doesn't clip
const LENS_R = 4.5;
const LENS_CY = [8.5, 17, 25.5] as const; // red, amber, green (housing frame)

export const SIGNAL_HEAD = {
  lensRadiusPx: LENS_R,
  // Lens-center offsets from the image center (red, amber, green); the
  // lens circle layers use these × icon-size as their circle-translate.
  lensOffsetYPx: [
    PAD + LENS_CY[0] - (H + 2 * PAD) / 2,
    PAD + LENS_CY[1] - (H + 2 * PAD) / 2,
    PAD + LENS_CY[2] - (H + 2 * PAD) / 2,
  ] as const,
} as const;

// signalHeadImage draws the unlit head: dark rounded housing with a light
// rim (it must read against the navy canvas) and three dim lenses. Drawn
// at devicePixelRatio so the housing stays as crisp as the lens circles on
// hiDPI screens (map.addImage defaults pixelRatio 1 otherwise).
export function signalHeadImage(): { image: ImageData; pixelRatio: number } {
  const dpr = Math.max(1, Math.min(3, Math.floor(globalThis.devicePixelRatio || 1)));
  const w = (W + 2 * PAD) * dpr;
  const h = (H + 2 * PAD) * dpr;
  const canvas = document.createElement("canvas");
  canvas.width = w;
  canvas.height = h;
  const ctx = canvas.getContext("2d");
  if (!ctx) throw new Error("signalhead: 2d canvas context unavailable");
  ctx.scale(dpr, dpr);

  const r = 4;
  ctx.beginPath();
  ctx.roundRect(PAD, PAD, W, H, r);
  ctx.fillStyle = "#0a1230";
  ctx.fill();
  ctx.strokeStyle = "rgba(214, 225, 255, 0.55)";
  ctx.lineWidth = 1;
  ctx.stroke();

  for (const cy of LENS_CY) {
    ctx.beginPath();
    ctx.arc(PAD + W / 2, PAD + cy, LENS_R, 0, 2 * Math.PI);
    ctx.fillStyle = "#1d2950";
    ctx.fill();
  }
  return { image: ctx.getImageData(0, 0, w, h), pixelRatio: dpr };
}
