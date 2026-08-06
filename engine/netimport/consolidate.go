package netimport

// consolidate.go — junction-cluster consolidation (ADR-0038).
//
// OSM maps divided intersections as one node per carriageway crossing, so the
// SUMO source contains junctions meters apart connected by edges whose lanes
// netconvert clamps between the two junction polygons: an 11.4 m geometric
// edge compiles to 0.2 m of usable road lane. Such a "sliver" lane carries
// less than one vehicle of storage, and the kernel's box-exit rule
// (exitWalk: vehicle length + s0 of clear room, stop at the first queue tail)
// reads ANY vehicle stopped on it as a capacity seal of the upstream box.
// Measured on chi-loop-urban (stranding-02): chains of sliver-coupled
// junctions stranded 1,588 vehicles in the valid 6 h drain baseline.
//
// The consolidation, per the ADR: a road lane shorter than sliverMaxLengthM
// whose successors are ALL junction-internal (it sits at a box entry with
// sub-vehicle storage) is deleted; each predecessor is rewired into the
// sliver's successors (the far box's internal lanes, already single-successor
// at emission time), and each successor internal is extended by the sliver's
// length and shape — the stop line moves upstream by the sliver length, which
// is the intended behavior change: the cluster behaves as one box, and a
// vehicle that would have parked on a 0.2 m lane now waits at a proper stop
// line on a real internal lane, visible to that junction's conflict sets.
//
// Notes on the edges of the rule:
//   - The threshold reads the lane's ORIGINAL parsed length, so a chain of
//     short connectors (junction → 2 m → junction → 3 m → junction)
//     consolidates recursively: each deletion prepends its length onto its
//     successors, and the outer fixpoint loop re-tests until no lane
//     qualifies. Termination is guaranteed (every pass deletes ≥1 lane).
//   - A rewired predecessor can inherit several successors (the sliver's
//     movement fan-out into the far box). For a road predecessor that is
//     ordinary diverge wiring; for an internal predecessor it produces a
//     MULTI-successor internal lane — new in practice, but the format never
//     enforced single-successor (netfile loader) and the kernel's
//     pickSuccessor/exitWalk probes are successor-list general. The far
//     internals themselves stay single-successor, which is the invariant the
//     kernel's comments lean on.
//   - Origin/exit/endWall lanes are never candidates (demand-facing lanes
//     are never deleted; verified on chi-loop-urban: no demand origin or
//     destination lane is <5 m).
//   - Everything iterates in file order and deletion order is recorded, so
//     the transform is deterministic (ADR-0005): same input → same bytes.

import (
	"traffic-sim/engine"
)

// sliverMaxLengthM is the consolidation threshold (ADR-0038: "~5 m"): below
// it a junction-connector lane holds less than one car (4–5 m body + 2 m jam
// gap) and acts as zero storage between two boxes.
const sliverMaxLengthM = 5.0

// extRec tracks one far internal's extension state: which sliver last
// extended it, the total length added, and the shape points prepended — the
// bookkeeping the longer-only parallel-extension guard needs to REPLACE a
// shorter sliver's contribution instead of summing both.
type extRec struct {
	sliver string
	length float64
	points int
}

// consolidateSlivers deletes sub-threshold junction-connector lanes from nf,
// rewiring their predecessors into the far junction's internal lanes and
// extending those internals by the sliver length. Deleted lane IDs are
// appended to rep.ConsolidatedSlivers in deletion order. Runs after the
// signal pass (successors, junctions, bindings all final) and before foe
// computation (which must see the extended geometry and rewired successors).
func consolidateSlivers(nf *engine.NetFile, rep *Report) {
	idx := map[string]int{}
	origLen := map[string]float64{}
	origSucc := map[string][]string{} // successors at pass start: chain vs parallel extension audit
	preds := map[string][]string{}
	for i := range nf.Lanes {
		l := &nf.Lanes[i]
		idx[l.ID] = i
		origLen[l.ID] = l.Length
		origSucc[l.ID] = append([]string(nil), l.Successors...)
	}
	for i := range nf.Lanes {
		for _, s := range nf.Lanes[i].Successors {
			preds[s] = append(preds[s], nf.Lanes[i].ID)
		}
	}
	deleted := map[string]bool{}
	extBy := map[string]extRec{} // far internal id -> extension(s) applied so far

	// isSliver reports whether lane l is a deletable junction connector:
	// short, not demand-facing, and feeding only junction-internal lanes.
	// Successor lists never dangle (deletions rewrite them eagerly), so the
	// all-internal test is always evaluated against live lanes.
	isSliver := func(l *engine.NetLane) bool {
		if l.Internal || l.Origin || l.Exit || l.EndWall {
			return false
		}
		if origLen[l.ID] >= sliverMaxLengthM || len(l.Successors) == 0 {
			return false
		}
		for _, s := range l.Successors {
			if s == l.ID || !nf.Lanes[idx[s]].Internal {
				return false
			}
		}
		return true
	}

	for {
		progress := false
		for i := range nf.Lanes {
			s := &nf.Lanes[i]
			if deleted[s.ID] || !isSliver(s) {
				continue
			}
			succs := append([]string(nil), s.Successors...)
			sPreds := append([]string(nil), preds[s.ID]...)
			// Rewire every predecessor: the sliver's slot in its successor
			// list becomes the far box's internal lanes, in place and in
			// order; duplicates collapse on first occurrence.
			for _, pid := range sPreds {
				p := &nf.Lanes[idx[pid]]
				out := make([]string, 0, len(p.Successors)+len(succs))
				seen := map[string]bool{}
				for _, c := range p.Successors {
					if c == s.ID {
						for _, x := range succs {
							if !seen[x] {
								seen[x] = true
								out = append(out, x)
							}
						}
						continue
					}
					if !seen[c] {
						seen[c] = true
						out = append(out, c)
					}
				}
				p.Successors = out
			}
			// Extend each far internal by the sliver's length and shape
			// (stop line moves upstream; a chain accumulates, which is the
			// recursive case working as intended). Two INDEPENDENT slivers
			// feeding the same internal do NOT sum: their approaches share
			// no geometry, so the LONGER sliver's extension replaces the
			// shorter one's (a shorter parallel sliver contributes nothing),
			// and the audit counter records every such choice (round-2
			// blocker; a chain's sequential extensions are legitimate and
			// stay quiet).
			for _, x := range succs {
				xl := &nf.Lanes[idx[x]]
				if prev, seen := extBy[x]; seen && !succOf(origSucc[s.ID], prev.sliver) {
					if !succOf(rep.SharedExtensions, x) {
						rep.SharedExtensions = append(rep.SharedExtensions, x)
					}
					if s.Length > prev.length {
						base := xl.Shape[prev.points:]
						newShape := prependShape(s.Shape, base)
						xl.Length += s.Length - prev.length
						xl.Shape = newShape
						extBy[x] = extRec{s.ID, s.Length, len(newShape) - len(base)}
					}
				} else {
					old := len(xl.Shape)
					xl.Shape = prependShape(s.Shape, xl.Shape)
					xl.Length += s.Length
					rec := extBy[x]
					extBy[x] = extRec{s.ID, rec.length + s.Length, rec.points + len(xl.Shape) - old}
				}
				// Successor-side pred bookkeeping: x's predecessors are now
				// the sliver's predecessors (file order), so a later
				// candidate's rewire sees the live graph.
				pl := preds[x]
				out := make([]string, 0, len(pl)+len(sPreds))
				seen := map[string]bool{}
				for _, q := range pl {
					if q == s.ID {
						for _, sp := range sPreds {
							if !seen[sp] {
								seen[sp] = true
								out = append(out, sp)
							}
						}
						continue
					}
					if !seen[q] {
						seen[q] = true
						out = append(out, q)
					}
				}
				preds[x] = out
			}
			deleted[s.ID] = true
			rep.ConsolidatedSlivers = append(rep.ConsolidatedSlivers, s.ID)
			progress = true
		}
		if !progress {
			break
		}
	}

	if len(rep.ConsolidatedSlivers) > 0 {
		kept := make([]engine.NetLane, 0, len(nf.Lanes)-len(deleted))
		for i := range nf.Lanes {
			if !deleted[nf.Lanes[i].ID] {
				kept = append(kept, nf.Lanes[i])
			}
		}
		nf.Lanes = kept
	}

	// Audit (round-3 review question): chains internal → UNCONTROLLED
	// internal → controlled internal across junction boundaries. At such a
	// chain the seam gate cannot see the downstream stop line from the
	// upstream box (it checks only the immediate successor), so a vehicle
	// would discover the hold one box late. Zero on the consolidated chi
	// import (2026-08-05); counted here so a future import that produces
	// one says so instead of silently changing gate coverage.
	rep.UncontrolledSeamChains = countUncontrolledSeamChains(nf)
}

// countUncontrolledSeamChains counts cross-junction successor chains of the
// shape internal → uncontrolled internal (no signal, no row class) →
// controlled internal (signal-bound or any row class), the one boundary
// pattern the seam gate does not cover.
func countUncontrolledSeamChains(nf *engine.NetFile) int {
	idx := map[string]*engine.NetLane{}
	for i := range nf.Lanes {
		idx[nf.Lanes[i].ID] = &nf.Lanes[i]
	}
	controlled := func(l *engine.NetLane) bool {
		return l.TL != "" || l.Row != ""
	}
	n := 0
	for i := range nf.Lanes {
		l1 := &nf.Lanes[i]
		if !l1.Internal {
			continue
		}
		for _, s1 := range l1.Successors {
			l2 := idx[s1]
			if l2 == nil || !l2.Internal || l2.Junction == l1.Junction || controlled(l2) {
				continue
			}
			for _, s2 := range l2.Successors {
				l3 := idx[s2]
				if l3 != nil && l3.Internal && l3.Junction != l2.Junction && controlled(l3) {
					n++
				}
			}
		}
	}
	return n
}

// succOf reports whether x is an element of xs.
func succOf(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

// prependShape returns post with pre's polyline prepended, collapsing the
// shared joint point when the sliver's end and the internal's start coincide.
// A small geometric seam is harmless either way: projection interpolates by
// arc length and geometry never feeds the dynamics or the CRC.
func prependShape(pre, post [][2]float64) [][2]float64 {
	if len(pre) > 0 && len(post) > 0 && pre[len(pre)-1] == post[0] {
		post = post[1:]
	}
	out := make([][2]float64, 0, len(pre)+len(post))
	out = append(out, pre...)
	return append(out, post...)
}

// computeConflictFoes annotates every internal lane with its conflict foes
// (ADR-0010): internal lanes of the same junction whose paths cross (shape
// polylines properly intersect) or merge (share ANY successor lane — the
// junction-exit funnels where overlaps were observed; successor-set
// intersection rather than first-successor equality since ADR-0038
// consolidation can leave internals with a rewired fan-out). Merge takes
// precedence when both hold. Runs AFTER consolidation so extended internal
// geometry and rewired successors are what the tests see.
func computeConflictFoes(nf *engine.NetFile, rep *Report) {
	byJunction := map[string][]int{} // junction id → internal lane positions, file order
	for i := range nf.Lanes {
		nl := &nf.Lanes[i]
		if nl.Internal {
			byJunction[nl.Junction] = append(byJunction[nl.Junction], i)
		}
	}
	// Map iteration order is irrelevant here: junctions' internal-lane sets
	// are disjoint, and per-lane appends happen in fixed (file, a, b) order,
	// so the output does not depend on which junction is visited first.
	for _, idxs := range byJunction {
		for a := 0; a < len(idxs); a++ {
			for b := a + 1; b < len(idxs); b++ {
				la, lb := &nf.Lanes[idxs[a]], &nf.Lanes[idxs[b]]
				merge := sharedSuccessor(la, lb)
				switch {
				case merge:
					la.FoesMerge = append(la.FoesMerge, lb.ID)
					lb.FoesMerge = append(lb.FoesMerge, la.ID)
					rep.ConflictPairs++
				case polylinesCross(la.Shape, lb.Shape):
					la.FoesCross = append(la.FoesCross, lb.ID)
					lb.FoesCross = append(lb.FoesCross, la.ID)
					rep.ConflictPairs++
				}
			}
		}
	}
}

// sharedSuccessor reports whether two lanes can funnel into a common lane.
func sharedSuccessor(la, lb *engine.NetLane) bool {
	for _, sa := range la.Successors {
		for _, sb := range lb.Successors {
			if sa == sb {
				return true
			}
		}
	}
	return false
}
