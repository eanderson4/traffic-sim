package natsio

// tsib.go — TSIB v1, the batched intent message (ADR-0026): one message per
// controller per tick on the SAME subject as per-vehicle v2 intents, demuxed
// by the intent_encoding NATS header in onIntent. Batching is a wire
// boundary change only: a valid batch expands into ArrivedIntent entries in
// record order and everything downstream (claim filter, seq/grant stamping,
// hold-last, the record plane) is identical to the v2 path.
//
// Wire format (little-endian):
//
//	header (24 B):  magic "TSIB" | version u16 = 1 | flags u16 (reserved, 0)
//	                | tick u64 | count u32 | reserved u32
//	records:        count × 44 B, byte-identical layout to the fixed section
//	                of intent v2 (shared helpers in bus.go — no duplicated
//	                layout arithmetic)
//
// tick is informational only (diagnostics, the M3 applied-lag metric): never
// validated, never gating acceptance — ingest is arrival-based exactly as
// for v2. Whole-batch structural validity: exact length (24 + 44·count),
// version 1, count ≤ TSIBMaxRecords, and the route-field rule — ANY record
// with route_len ≠ 0 or flag bit4 set makes the WHOLE batch invalid (route
// updates go as one complete standalone v2 intent, and the vehicle is
// omitted from that tick's batch). A structurally invalid batch is dropped
// whole, never partially applied. Per-record SEMANTIC checks are unchanged:
// a NaN/Inf accel/setpoint drops ONLY that record, in parity with a bad v2
// message dropping alone.

import (
	"encoding/binary"

	"traffic-sim/engine"
)

const (
	tsibHeaderBytes = 24

	// TSIBMaxRecords caps one batch (20,000 records = 880,024 B with the
	// header): under the 1 MiB per-message discipline with ~32 B of headroom
	// past the theoretical ceiling, pinned by a boundary publish test. A
	// controller with more claimed vehicles splits into multiple batches
	// per tick.
	TSIBMaxRecords = 20000
)

// EncodeTSIB serializes one batch for ts.{run}.ctl.intent.{controller_id}
// (published with the intent_encoding: tsib header). Route-bearing intents
// (RouteSet) are SKIPPED — a batch never carries a route field; the driver
// diverts those vehicles to standalone v2 intents for the tick (ADR-0026;
// the diversion itself is M2 driver work). Precondition: at most
// TSIBMaxRecords route-free intents — over that, EncodeTSIB returns nil
// rather than emitting invalid-by-construction wire bytes; splitting at the
// cap is the driver's job (M2).
func EncodeTSIB(tick uint64, intents []engine.Intent) []byte {
	n := 0
	for _, in := range intents {
		if !in.RouteSet {
			n++
		}
	}
	if n > TSIBMaxRecords {
		return nil
	}
	buf := make([]byte, tsibHeaderBytes+intentFixedBytes*n)
	copy(buf[0:], "TSIB")
	binary.LittleEndian.PutUint16(buf[4:], 1) // version; flags u16 reserved 0
	binary.LittleEndian.PutUint64(buf[8:], tick)
	off := tsibHeaderBytes
	var count uint32
	for _, in := range intents {
		if in.RouteSet {
			continue
		}
		putIntentFixed(buf[off:], in, intentFixedFlags(in), 0)
		off += intentFixedBytes
		count++
	}
	binary.LittleEndian.PutUint32(buf[16:], count)
	return buf[:off]
}

// DecodeTSIB parses one batch into its surviving records, in record order.
// ok=false means the batch is structurally invalid and must be dropped WHOLE
// (counted intentBatchDropped), never partially applied. ok=true may return
// fewer than count records: a NaN/Inf accel/setpoint drops only its own
// record (v2 parity), counted in recordDrops (the batch-level parity of
// v2's dropped counter). An empty batch (count 0) is valid and returns
// nothing.
func DecodeTSIB(buf []byte) (intents []engine.Intent, recordDrops int, ok bool) {
	if len(buf) < tsibHeaderBytes {
		return nil, 0, false
	}
	if string(buf[0:4]) != "TSIB" || binary.LittleEndian.Uint16(buf[4:]) != 1 {
		return nil, 0, false
	}
	count := binary.LittleEndian.Uint32(buf[16:])
	if count > TSIBMaxRecords || len(buf) != tsibHeaderBytes+intentFixedBytes*int(count) {
		return nil, 0, false
	}
	out := make([]engine.Intent, 0, count)
	off := tsibHeaderBytes
	for i := uint32(0); i < count; i++ {
		var in engine.Intent
		flags, routeLen := getIntentFixed(buf[off:], &in)
		off += intentFixedBytes
		if routeLen != 0 || flags&intentFlagRouteSet != 0 {
			// Routes are forbidden in batch records: structural rejection
			// of the WHOLE batch (ADR-0026 — never partially applied).
			return nil, 0, false
		}
		if !applyIntentFlags(&in, flags) {
			recordDrops++ // NaN/Inf axis: this record alone drops (v2 parity)
			continue
		}
		out = append(out, in)
	}
	return out, recordDrops, true
}
