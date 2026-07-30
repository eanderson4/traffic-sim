package natsio

import (
	"encoding/binary"
	"fmt"

	"traffic-sim/engine"
)

// tslb.go — TSLB v1, the batched intent log record (ADR-0035).
//
// The record plane wrote one JetStream message per applied intent, which is
// one message per vehicle per tick. ADR-0026 batched the LIVE intent path
// into TSIB frames and deliberately scoped the record plane out ("the record
// plane never sees a batch"), so the recorder expanded each batch back into
// individual messages. Measured on the shipped chihalf recording: an 8 MiB
// stream block held 34,794 intent messages over 10 ticks — ~230 bytes per
// message for a record whose fixed section is 61 bytes. The difference is the
// subject string and JetStream's per-record framing, re-paid ~3,500 times a
// tick. The whole 90-minute recording is 48 GiB, essentially all of it this.
//
// TSLB carries one tick's records in one message. The records are the v2
// per-message payloads BYTE-IDENTICAL and in the same order, so this is a
// framing change and not a re-encoding: `appendLoggedIntent` writes both forms
// and `decodeLoggedIntentAt` reads both. That is deliberate — it makes "the
// batched log holds exactly what the per-message log held" a claim a test can
// check by construction rather than a property to be argued.
//
// Per-record density (hoisting the repeated applied_tick and controller name
// out of every record, as TSIB does with its fixed 44 B records) is left
// undone ON PURPOSE. Those repeated bytes are exactly what a compressor eats:
// measured on a real stream block, gzip -3 gets 6.2x and zstd -3 gets 7.2x,
// and ADR-0035 turns on JetStream S2 storage compression in the same change.
// Hoisting them would buy maybe 20% on top of that while introducing a
// controller-index table and a route indirection — new invariants on the one
// plane where a decode bug is unrecoverable, since the record IS the run.
//
//	offset  size  field
//	     0     4  magic 'T','S','L','B'
//	     4     1  version = 1
//	     5     1  flags (reserved, must be 0)
//	     6     4  count — records in THIS message (little endian)
//	    10     8  tick — the applied_tick every record shares
//	    18     -  count × v2 intent record (see recorder.go)
//
// A tick whose records outgrow the byte budget is split across several TSLB
// messages, each independently decodable and each naming the same tick. That
// needs no chunk index (unlike ADR-0015 keyframes, which reassemble ONE blob)
// because concatenating the records of consecutive messages in stream order is
// exactly the original order.
const (
	tslbHeader  = 18
	tslbVersion = 1

	// loggedIntentMin is the smallest possible v2 record: the 18-byte prefix
	// plus the 43-byte fixed section, with an empty controller name and no
	// route. Used to bound a batch's declared count against the bytes
	// actually present before trusting it.
	loggedIntentMin = 18 + 43
)

var tslbMagic = [4]byte{'T', 'S', 'L', 'B'}

// beginTSLB appends a TSLB header with count and tick left zero, to be filled
// by finishTSLB once the record count is known.
func beginTSLB(dst []byte) []byte {
	dst = append(dst, tslbMagic[0], tslbMagic[1], tslbMagic[2], tslbMagic[3])
	dst = append(dst, tslbVersion, 0)
	dst = binary.LittleEndian.AppendUint32(dst, 0) // count
	dst = binary.LittleEndian.AppendUint64(dst, 0) // tick
	return dst
}

// finishTSLB stamps count and tick into a buffer started by beginTSLB.
func finishTSLB(buf []byte, tick uint64, count int) []byte {
	binary.LittleEndian.PutUint32(buf[6:], uint32(count))
	binary.LittleEndian.PutUint64(buf[10:], tick)
	return buf
}

// DecodeTSLB parses one batch into its records, in record order.
//
// Strict: an unknown magic or version, a count that disagrees with the bytes
// present, or trailing bytes after the last record are all errors. The record
// plane is the run — a batch that does not decode exactly must fail the
// replay loudly rather than yield a short tick that still simulates and still
// produces plausible numbers.
func DecodeTSLB(buf []byte) ([]engine.TickedIntent, error) {
	if len(buf) < tslbHeader {
		return nil, fmt.Errorf("TSLB: %d bytes, shorter than the %d-byte header", len(buf), tslbHeader)
	}
	if buf[0] != tslbMagic[0] || buf[1] != tslbMagic[1] ||
		buf[2] != tslbMagic[2] || buf[3] != tslbMagic[3] {
		return nil, fmt.Errorf("TSLB: bad magic %q", buf[:4])
	}
	if buf[4] != tslbVersion {
		return nil, fmt.Errorf("TSLB: version %d, this build knows %d", buf[4], tslbVersion)
	}
	if buf[5] != 0 {
		return nil, fmt.Errorf("TSLB: reserved flags byte is %#x, want 0", buf[5])
	}
	count := int(binary.LittleEndian.Uint32(buf[6:]))
	tick := binary.LittleEndian.Uint64(buf[10:])
	rest := buf[tslbHeader:]
	// count comes from the payload, so it is untrusted until the records
	// check out: a corrupt frame can declare ~4e9 records in an 18-byte
	// message, and preallocating from it would OOM the reader where every
	// other malformed input gets the loud error below. A record is at least
	// loggedIntentMin bytes, so a count the bytes cannot contain is already
	// corrupt — refuse it before sizing anything from it.
	if count > len(rest)/loggedIntentMin {
		return nil, fmt.Errorf("TSLB: count %d, only %d record bytes present", count, len(rest))
	}
	out := make([]engine.TickedIntent, 0, count)
	for i := 0; i < count; i++ {
		t, n, err := decodeLoggedIntentAt(rest)
		if err != nil {
			return nil, fmt.Errorf("TSLB record %d/%d: %w", i+1, count, err)
		}
		if t.Tick != tick {
			// The header tick and the records must agree. Disagreement means
			// a writer mixed ticks or the payload is corrupt; either way the
			// intents would be applied at the wrong tick on replay.
			return nil, fmt.Errorf("TSLB record %d/%d: applied_tick %d, batch tick %d",
				i+1, count, t.Tick, tick)
		}
		out = append(out, t)
		rest = rest[n:]
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("TSLB: %d trailing bytes after %d records", len(rest), count)
	}
	return out, nil
}
