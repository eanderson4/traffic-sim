// tssf.test.ts — decoder tests against (a) golden frames produced by the
// Go encoder (engine/natsio/frame.go SnapshotFrame, ring network,
// placeholder projection) and (b) hand-built fixtures covering u64 ids,
// negative f32 fields, and every rejection path.

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  decodeFrame,
  TSSF_HEADER_BYTES,
  TSSF_VEHICLE_BYTES,
  TSSF_MAGIC,
  TSSF_VERSION,
} from "../src/tssf.ts";

// Golden: `go test ./natsio/ -run TestGoldenGen` output (temporary generator,
// ring net, InitialVehicles 2, seed 1). Frame at tick 0: vehicle 1 at s=0,
// vehicle 2 at s=115 (230 m lane / 2).
const GO_FRAME_TICK0 =
  "54535346010000000000000000000000020000000000000001000000000000000000000000000000000000000000000002000000000000000000e642000000000000000000000000";
// Same run after 10 steps; Go ParseFrame decoded: id=1 x=8.29290009,
// id=2 x=123.2929 (y/angle/class 0).
const GO_FRAME_TICK10 =
  "54535346010000000a0000000000000002000000000000000100000000000000b8af04410000000000000000000000000200000000000000f795f642000000000000000000000000";

function fromHex(hex: string): Uint8Array {
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(hex.slice(2 * i, 2 * i + 2), 16);
  return out;
}

// buildFrame is an independent writer for the TSSF v1 layout (fixtures the
// Go golden can't express, e.g. huge u64 ids).
function buildFrame(
  tick: bigint,
  vehicles: Array<{ id: bigint; x: number; y: number; angle: number; cls: number }>,
): Uint8Array {
  const buf = new ArrayBuffer(TSSF_HEADER_BYTES + vehicles.length * TSSF_VEHICLE_BYTES);
  const dv = new DataView(buf);
  dv.setUint32(0, TSSF_MAGIC, true);
  dv.setUint16(4, TSSF_VERSION, true);
  dv.setUint16(6, 0, true);
  dv.setBigUint64(8, tick, true);
  dv.setUint32(16, vehicles.length, true);
  let off = TSSF_HEADER_BYTES;
  for (const v of vehicles) {
    dv.setBigUint64(off, v.id, true);
    dv.setFloat32(off + 8, v.x, true);
    dv.setFloat32(off + 12, v.y, true);
    dv.setFloat32(off + 16, v.angle, true);
    dv.setFloat32(off + 20, v.cls, true);
    off += TSSF_VEHICLE_BYTES;
  }
  return new Uint8Array(buf);
}

test("decodes the Go encoder's tick-0 frame exactly", () => {
  const f = decodeFrame(fromHex(GO_FRAME_TICK0));
  assert.equal(f.tick, 0);
  assert.equal(f.vehicles.length, 2);
  assert.deepEqual(f.vehicles[0], { id: 1, x: 0, y: 0, angle: 0, cls: 0 });
  assert.deepEqual(f.vehicles[1], { id: 2, x: 115, y: 0, angle: 0, cls: 0 });
});

test("decodes the Go encoder's tick-10 frame (f32 agreement)", () => {
  const f = decodeFrame(fromHex(GO_FRAME_TICK10));
  assert.equal(f.tick, 10);
  assert.equal(f.vehicles.length, 2);
  const [v1, v2] = f.vehicles;
  assert.equal(v1?.id, 1);
  assert.ok(Math.abs((v1?.x ?? 0) - 8.29290009) < 1e-5, `v1.x=${v1?.x}`);
  assert.equal(v1?.y, 0);
  assert.equal(v1?.angle, 0);
  assert.equal(v1?.cls, 0);
  assert.equal(v2?.id, 2);
  assert.ok(Math.abs((v2?.x ?? 0) - 123.2929) < 1e-5, `v2.x=${v2?.x}`);
});

test("decodes u64 ids/ticks beyond 2^32 and negative f32 fields", () => {
  const data = buildFrame(2n ** 33n + 7n, [
    { id: 2n ** 32n + 5n, x: -12.5, y: 3.75, angle: -Math.PI / 2, cls: 1 },
  ]);
  const f = decodeFrame(data);
  assert.equal(f.tick, 8589934599);
  assert.equal(f.vehicles.length, 1);
  const v = f.vehicles[0]!;
  assert.equal(v.id, 4294967301);
  assert.equal(v.x, -12.5);
  assert.equal(v.y, 3.75);
  assert.ok(Math.abs(v.angle + Math.PI / 2) < 1e-7);
  assert.equal(v.cls, 1);
});

test("decodes an empty frame", () => {
  const f = decodeFrame(buildFrame(42n, []));
  assert.equal(f.tick, 42);
  assert.deepEqual(f.vehicles, []);
});

test("rejects short, bad-magic, bad-version, and length-mismatched frames", () => {
  assert.throws(() => decodeFrame(new Uint8Array(10)), /at least 24/);
  const badMagic = buildFrame(0n, []);
  badMagic[0] = 0x99;
  assert.throws(() => decodeFrame(badMagic), /bad magic/);
  const badVersion = buildFrame(0n, []);
  new DataView(badVersion.buffer).setUint16(4, 2, true);
  assert.throws(() => decodeFrame(badVersion), /schema_version 2/);
  const truncated = buildFrame(0n, [{ id: 1n, x: 0, y: 0, angle: 0, cls: 0 }]).slice(0, 30);
  assert.throws(() => decodeFrame(truncated), /want 48 for 1 vehicles/);
  const padded = buildFrame(0n, []);
  const withExtra = new Uint8Array(padded.length + 1);
  withExtra.set(padded);
  assert.throws(() => decodeFrame(withExtra), /want 24 for 0 vehicles/);
});
