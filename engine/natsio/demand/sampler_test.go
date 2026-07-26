package demand

import (
	"testing"

	"traffic-sim/engine"
	"traffic-sim/engine/scenario"
)

// sampler_test.go — the determinism property ADR-0021 has to preserve: a
// flow that declares no destinations must consume EXACTLY the draws it
// consumed before the destination axis existed, or every pinned demand
// realization moves.

// TestDestinationDrawIsAppendOnly: the arrival program of a
// destination-free flow is unchanged by the ADR-0021 code path, and a flow
// WITH destinations keeps the same arrival times (the destination draw
// comes after the gap and type draws, so it cannot perturb them).
func TestDestinationDrawIsAppendOnly(t *testing.T) {
	base := scenario.Flow{
		Origin: "a_0", VehPerH: 900, Spacing: "poisson",
		VTypes: map[string]float64{"car": 0.9, "truck": 0.1},
	}
	routed := base
	routed.Destinations = map[string]float64{"b_0": 0.75, "c_0": 0.25}

	plainS := newFlowSampler(base, 42, 0)
	routedS := newFlowSampler(routed, 42, 0)
	for i := range 200 {
		atP, vtP, destP, okP := plainS.next(0.1)
		atR, vtR, destR, okR := routedS.next(0.1)
		if okP != okR || atP != atR || vtP != vtR {
			t.Fatalf("draw %d diverged: plain=(%v,%q,%v) routed=(%v,%q,%v) — the destination draw perturbed the arrival program",
				i, atP, vtP, okP, atR, vtR, okR)
		}
		if destP != "" {
			t.Fatalf("draw %d: destination-free flow produced %q", i, destP)
		}
		if destR != "b_0" && destR != "c_0" {
			t.Fatalf("draw %d: routed flow produced destination %q", i, destR)
		}
	}
}

// TestDestinationDrawDistribution: the weighted draw honors the weights and
// walks the SORTED key list, so it cannot depend on Go map order.
func TestDestinationDrawDistribution(t *testing.T) {
	weights := map[string]float64{"b_0": 3, "c_0": 1}
	counts := map[string]int{}
	for i := range 4000 {
		st := engine.DeriveStream(7, uint64(i))
		counts[pickWeighted(st, weights)]++
	}
	total := counts["b_0"] + counts["c_0"]
	if total != 4000 {
		t.Fatalf("draws produced %d in-set results out of 4000: %v", total, counts)
	}
	// 3:1 weights → b_0 near 75%. A wide band: this pins the weighting, not
	// the RNG's exact realization.
	if share := float64(counts["b_0"]) / float64(total); share < 0.72 || share > 0.78 {
		t.Errorf("b_0 share %.3f, want ≈0.75 for 3:1 weights (%v)", share, counts)
	}
	// An empty distribution draws nothing at all — and consumes no draw,
	// which is what keeps destination-free flows bit-identical.
	st := engine.DeriveStream(7, 1)
	before := st.Draws()
	if got := pickWeighted(st, nil); got != "" {
		t.Errorf("empty distribution returned %q, want \"\"", got)
	}
	if st.Draws() != before {
		t.Errorf("empty distribution consumed %d draws, want 0", st.Draws()-before)
	}
}
