package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// netfile.go — the compiled network file format v1
// (contracts/network-format-v1.md): a flat list of lanes with durable IDs,
// per-lane geometry, lane→lane successors, and provenance. The netconvert
// bootstrap (engine/cmd/netimport) writes it; the engine loads it via
// NetSpec{Kind: "file"}. It migrates into the scenario-format work later.
// Junction right-of-way is an optional v1 extension (ADR-0010): internal
// lanes may carry their junction, approach class, and conflict foes; files
// without them load with junctions unmodeled (free traversal), unchanged.
// Fixed-time signal programs are a second optional v1 extension
// (ADR-0011): a top-level "signals" list plus per-internal-lane tl/tlLink
// bindings; files without them load with all junctions unsignalized,
// unchanged.

// NetFile is the top-level document. Version gates the loader; Provenance
// records where the network came from (recipe, not just data — ADR-0009 §5).
type NetFile struct {
	Version    int            `json:"version"` // 1
	Name       string         `json:"name,omitempty"`
	Provenance *NetProvenance `json:"provenance,omitempty"`
	Lanes      []NetLane      `json:"lanes"`
	Signals    []NetSignal    `json:"signals,omitempty"` // fixed-time signal programs (ADR-0011)
}

// NetProvenance is the network-level import recipe and source geometry frame.
type NetProvenance struct {
	Source     string     `json:"source,omitempty"`     // importer, e.g. "netimport (netconvert 1.27.1 .net.xml)"
	SourceFile string     `json:"sourceFile,omitempty"` // e.g. "i280.net.xml"
	Imported   string     `json:"imported,omitempty"`   // RFC 3339 import date
	OSMBbox    string     `json:"osmBbox,omitempty"`    // Overpass bbox "S,W,N,E" when OSM-derived
	Projection string     `json:"projection,omitempty"` // projParameter of the local metric frame
	NetOffset  [2]float64 `json:"netOffset,omitempty"`  // netOffset of the local frame (m)
	Notes      string     `json:"notes,omitempty"`
}

// NetLane is one lane. Shape is the per-lane centerline polyline as [x,y]
// pairs in the local metric frame (meters). Source carries the two-tier
// identity provenance (ADR-0009 §3): our IDs are durable, source IDs ride
// along, and every default-filled field is named in Guessed.
type NetLane struct {
	ID         string       `json:"id"`
	Section    string       `json:"section"`              // metrics grouping (edge id; junction id inside junctions)
	Edge       string       `json:"edge,omitempty"`       // lateral chaining group; empty = no neighbors
	EdgeIndex  int          `json:"edgeIndex"`            // position within Edge, 0 = rightmost (SUMO convention)
	Length     float64      `json:"length"`               // m
	SpeedLimit float64      `json:"speedLimit"`           // m/s
	Width      float64      `json:"width"`                // m
	Shape      [][2]float64 `json:"shape"`                // centerline polyline [[x,y],...]; ≥2 points
	LatOffset  float64      `json:"latOffset,omitempty"`  // m, left-positive (0 for per-lane centerlines)
	Successors []string     `json:"successors,omitempty"` // lane IDs, ordered left-to-right (first = leftmost = default route)
	Origin     bool         `json:"origin,omitempty"`     // demand portal: the spawner injects here
	Exit       bool         `json:"exit,omitempty"`       // map edge: vehicles despawn past the end
	EndWall    bool         `json:"endWall,omitempty"`    // dead end: virtual standing vehicle at Length
	Internal   bool         `json:"internal,omitempty"`   // junction-internal lane (intersection interior)
	Source     *LaneSource  `json:"source,omitempty"`     // provenance

	// Junction right-of-way (ADR-0010, optional v1 extension — absent means
	// the junction is unmodeled and traversed freely, exactly as before).
	// Only set on internal lanes.
	Junction  string   `json:"junction,omitempty"`  // junction this internal lane belongs to
	Row       string   `json:"row,omitempty"`       // approach class: "major"|"minor"|"stop"
	FoesCross []string `json:"foesCross,omitempty"` // conflicting internal lanes (crossing paths)
	FoesMerge []string `json:"foesMerge,omitempty"` // conflicting internal lanes (same exit lane)

	// Fixed-time signal binding (ADR-0011, optional v1 extension): TL names
	// the program in NetFile.Signals, TLLink is this lane's index into its
	// phase state strings. Both or neither; internal lanes only. Absent
	// means the approach is unsignalized (pre-extension semantics).
	TL     string `json:"tl,omitempty"`
	TLLink *int   `json:"tlLink,omitempty"`
}

// NetSignal is one fixed-time signal program (ADR-0011): the junction's
// phase list in practitioner form (durations in seconds, per-link state
// strings in the SUMO tlLogic alphabet: g/G go, y amber, r red, o/O/u
// off/blinking). The kernel compiles durations onto the tick grid at engine
// build and derives the phase in force from the tick count.
type NetSignal struct {
	ID       string           `json:"id"`               // program id (the tlLogic id); unique per file
	Junction string           `json:"junction"`         // junction it serves
	Offset   float64          `json:"offset,omitempty"` // s; phase 0 begins at this sim time
	Phases   []NetSignalPhase `json:"phases"`           // in cycle order; ≥ 1
}

// NetSignalPhase is one phase: a duration (s, > 0) and the per-link state
// string. All phases of a program share the state-string length.
type NetSignalPhase struct {
	Duration float64 `json:"duration"` // s
	State    string  `json:"state"`    // per-link states, one char per link
}

// LaneSource is the per-lane provenance: where this lane came from and what
// the importer had to guess (ADR-0009 §3 two-tier identity).
type LaneSource struct {
	Edge    string   `json:"edge,omitempty"`    // source edge id (SUMO edge id; OSM way id rides in it per SUMO naming)
	Lane    int      `json:"lane"`              // source lane index within that edge
	Guessed []string `json:"guessed,omitempty"` // default-filled/heuristic fields: "width", "internal-geometry", ...
}

// LoadNetFile reads and compiles a network JSON file (format v1).
func LoadNetFile(path string) (*Network, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("netfile: %w", err)
	}
	var nf NetFile
	if err := json.Unmarshal(data, &nf); err != nil {
		return nil, fmt.Errorf("netfile %s: %w", path, err)
	}
	n, err := CompileNet(&nf)
	if err != nil {
		return nil, fmt.Errorf("netfile %s: %w", path, err)
	}
	return n, nil
}

// CompileNet validates a NetFile and builds the runtime Network. Lane order
// in the file is canonical (Lane.Index); lateral Left/Right links are derived
// from (Edge, EdgeIndex) groups — only consecutive-index lanes chain, so a
// filtered-out lane (e.g. a dropped sidewalk lane) never links across.
func CompileNet(nf *NetFile) (*Network, error) {
	if nf.Version != 1 {
		return nil, fmt.Errorf("unsupported network version %d (want 1)", nf.Version)
	}
	if len(nf.Lanes) == 0 {
		return nil, fmt.Errorf("network has no lanes")
	}
	n := &Network{byID: make(map[string]*Lane, len(nf.Lanes))}
	for i := range nf.Lanes {
		nl := &nf.Lanes[i]
		if nl.ID == "" {
			return nil, fmt.Errorf("lane #%d: empty id", i)
		}
		if _, dup := n.byID[nl.ID]; dup {
			return nil, fmt.Errorf("lane %s: duplicate id", nl.ID)
		}
		if nl.Length <= 0 {
			return nil, fmt.Errorf("lane %s: length %v (want > 0)", nl.ID, nl.Length)
		}
		if nl.SpeedLimit <= 0 {
			return nil, fmt.Errorf("lane %s: speedLimit %v (want > 0)", nl.ID, nl.SpeedLimit)
		}
		width := nl.Width
		if width == 0 {
			width = 3.5 // cosmetic in v1 physics; default rather than fail
		} else if width < 0 {
			return nil, fmt.Errorf("lane %s: width %v (want >= 0)", nl.ID, nl.Width)
		}
		l := &Lane{
			ID:         nl.ID,
			Section:    nl.Section,
			Index:      len(n.Lanes),
			Length:     nl.Length,
			SpeedLimit: nl.SpeedLimit,
			Width:      width,
			Exit:       nl.Exit,
			EndWall:    nl.EndWall,
			Internal:   nl.Internal,
		}
		if len(nl.Shape) > 0 {
			pts := make([]Point, len(nl.Shape))
			for j, p := range nl.Shape {
				pts[j] = Point{X: p[0], Y: p[1]}
			}
			l.SetShape(pts, nl.LatOffset)
			if l.Shape == nil {
				return nil, fmt.Errorf("lane %s: shape needs at least 2 points", nl.ID)
			}
		}
		n.Lanes = append(n.Lanes, l)
		n.byID[l.ID] = l
	}

	// Resolve successors and enforce the end-of-lane contract.
	for i := range nf.Lanes {
		nl := &nf.Lanes[i]
		l := n.Lanes[i]
		for _, sid := range nl.Successors {
			succ, ok := n.byID[sid]
			if !ok {
				return nil, fmt.Errorf("lane %s: unknown successor %q", nl.ID, sid)
			}
			if succ == l {
				return nil, fmt.Errorf("lane %s: self-successor (file networks are open; use the in-code ring for loops)", nl.ID)
			}
			l.Successors = append(l.Successors, succ)
		}
		switch {
		case len(l.Successors) > 0 && (l.Exit || l.EndWall):
			return nil, fmt.Errorf("lane %s: successors on an exit/endWall lane", nl.ID)
		case len(l.Successors) == 0 && !l.Exit && !l.EndWall:
			return nil, fmt.Errorf("lane %s: no successors and neither exit nor endWall (dangling lane)", nl.ID)
		case l.Exit && l.EndWall:
			return nil, fmt.Errorf("lane %s: both exit and endWall", nl.ID)
		}
	}

	// Junction right-of-way (ADR-0010): annotate internal lanes, resolve
	// conflict foes. Files predating the extension carry no fields — every
	// lane keeps RowNone and junctions are traversed freely, unchanged.
	for i := range nf.Lanes {
		nl := &nf.Lanes[i]
		l := n.Lanes[i]
		row, err := ParseRowState(nl.Row)
		if err != nil {
			return nil, fmt.Errorf("lane %s: %w", nl.ID, err)
		}
		if !nl.Internal && (nl.Row != "" || nl.Junction != "" || len(nl.FoesCross) > 0 || len(nl.FoesMerge) > 0) {
			return nil, fmt.Errorf("lane %s: right-of-way fields on a non-internal lane", nl.ID)
		}
		l.Junction, l.Row = nl.Junction, row
		for _, fid := range nl.FoesCross {
			foe, ok := n.byID[fid]
			if !ok {
				return nil, fmt.Errorf("lane %s: unknown foesCross lane %q", nl.ID, fid)
			}
			if !foe.Internal {
				return nil, fmt.Errorf("lane %s: foesCross lane %q is not internal", nl.ID, fid)
			}
			l.FoesCross = append(l.FoesCross, foe)
		}
		for _, fid := range nl.FoesMerge {
			foe, ok := n.byID[fid]
			if !ok {
				return nil, fmt.Errorf("lane %s: unknown foesMerge lane %q", nl.ID, fid)
			}
			if !foe.Internal {
				return nil, fmt.Errorf("lane %s: foesMerge lane %q is not internal", nl.ID, fid)
			}
			l.FoesMerge = append(l.FoesMerge, foe)
		}
	}

	// Fixed-time signal programs (ADR-0011): build the runtime programs,
	// then bind internal lanes to (program, link index). Files predating
	// the extension carry no "signals" and no tl fields — every lane keeps
	// a nil Signal and junctions behave exactly as before.
	if len(nf.Signals) > 0 {
		programs := make(map[string]*SignalProgram, len(nf.Signals))
		for i := range nf.Signals {
			ns := &nf.Signals[i]
			if _, dup := programs[ns.ID]; dup {
				return nil, fmt.Errorf("signal program %s: duplicate id", ns.ID)
			}
			phases := make([]SignalPhase, len(ns.Phases))
			for j, ph := range ns.Phases {
				phases[j] = SignalPhase{Duration: ph.Duration, State: ph.State}
			}
			p, err := newSignalProgram(ns.ID, ns.Junction, ns.Offset, phases)
			if err != nil {
				return nil, err
			}
			programs[p.ID] = p
			n.Signals = append(n.Signals, p)
		}
		for i := range nf.Lanes {
			nl := &nf.Lanes[i]
			l := n.Lanes[i]
			if nl.TL == "" && nl.TLLink == nil {
				continue
			}
			if !nl.Internal {
				return nil, fmt.Errorf("lane %s: signal binding (tl/tlLink) on a non-internal lane", nl.ID)
			}
			if nl.TL == "" || nl.TLLink == nil {
				return nil, fmt.Errorf("lane %s: tl and tlLink must appear together", nl.ID)
			}
			p, ok := programs[nl.TL]
			if !ok {
				return nil, fmt.Errorf("lane %s: unknown signal program %q", nl.ID, nl.TL)
			}
			if *nl.TLLink < 0 || *nl.TLLink >= len(p.Phases[0].State) {
				return nil, fmt.Errorf("lane %s: tlLink %d out of range (program %s has %d links)", nl.ID, *nl.TLLink, nl.TL, len(p.Phases[0].State))
			}
			l.Signal, l.LinkIdx = p, *nl.TLLink
		}
	} else {
		for i := range nf.Lanes {
			if nf.Lanes[i].TL != "" || nf.Lanes[i].TLLink != nil {
				return nil, fmt.Errorf("lane %s: signal binding (tl/tlLink) without any signals program", nf.Lanes[i].ID)
			}
		}
	}

	// Lateral chaining: same-edge lanes in EdgeIndex order, consecutive
	// indices only (0 = rightmost; left neighbor = index+1).
	byEdge := map[string][]int{}
	for i := range nf.Lanes {
		if nf.Lanes[i].Edge != "" {
			byEdge[nf.Lanes[i].Edge] = append(byEdge[nf.Lanes[i].Edge], i)
		}
	}
	for edge, idxs := range byEdge {
		sort.Slice(idxs, func(a, b int) bool { return nf.Lanes[idxs[a]].EdgeIndex < nf.Lanes[idxs[b]].EdgeIndex })
		seen := map[int]bool{}
		for _, i := range idxs {
			if seen[nf.Lanes[i].EdgeIndex] {
				return nil, fmt.Errorf("edge %s: duplicate lane index %d", edge, nf.Lanes[i].EdgeIndex)
			}
			seen[nf.Lanes[i].EdgeIndex] = true
		}
		for k := 0; k+1 < len(idxs); k++ {
			a, b := idxs[k], idxs[k+1]
			if nf.Lanes[b].EdgeIndex != nf.Lanes[a].EdgeIndex+1 {
				continue // gap (filtered lane): not physically adjacent
			}
			la, lb := n.Lanes[a], n.Lanes[b]
			if la.Length != lb.Length {
				return nil, fmt.Errorf("edge %s: lanes %s (%.2f m) and %s (%.2f m) differ in length (lateral neighbors must span the same s range)", edge, la.ID, la.Length, lb.ID, lb.Length)
			}
			la.Left, lb.Right = lb, la
		}
	}

	for i := range nf.Lanes {
		if nf.Lanes[i].Origin {
			n.Origins = append(n.Origins, n.Lanes[i])
		}
	}
	linkPrevs(n)
	return n, nil
}
