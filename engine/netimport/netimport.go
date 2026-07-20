// Package netimport converts a SUMO .net.xml network (netconvert's output)
// into the compiled network file format v1 (contracts/network-format-v1.md).
// It is the netconvert bootstrap of ADR-0009 §1: netconvert runs as a binary
// preprocessor (OSM → .net.xml), this tool compiles .net.xml → our durable
// network JSON. Standard library only (encoding/xml, encoding/json).
//
// Scope (v1, per ADR-0009 §6 and the milestone): edges/lanes with their
// shape polylines, lane→lane connections (including junction-internal
// "internal" edges), priority-junction right-of-way (ADR-0010: connection
// state → approach classes, conflict foes from internal-lane geometry),
// and fixed-time signal programs (ADR-0011: static tlLogic → phase lists,
// connections → per-lane link bindings). Explicit NON-goals: non-static
// programs (actuated/NEMA — reported, approaches stay unsignalized), no
// turn-restriction relations beyond what the .net.xml connection elements
// already encode.
package netimport

import (
	"encoding/xml"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"traffic-sim/engine"
)

// Options parameterizes a conversion.
type Options struct {
	Name       string // network name (default: derived from SourceFile)
	SourceFile string // .net.xml file name, for provenance
	Source     string // importer stamp, e.g. "netimport (netconvert 1.27.1 .net.xml)"
	Imported   string // RFC 3339 import date
	OSMBbox    string // Overpass bbox "S,W,N,E" when the .net.xml is OSM-derived
}

// Report is the auditable import summary (ADR-0009 §3: the import report
// ships as an artifact). Everything heuristic or dropped is listed here.
type Report struct {
	Lanes                int      `json:"lanes"`
	InternalLanes        int      `json:"internalLanes"`
	Connections          int      `json:"connections"`
	Origins              int      `json:"origins"`
	Exits                int      `json:"exits"`
	SkippedEdges         []string `json:"skippedEdges,omitempty"` // non-motor edges (rail-only etc.)
	SkippedLanes         []string `json:"skippedLanes,omitempty"` // non-motor lanes on kept edges (sidewalks, bike lanes)
	DroppedConnections   []string `json:"droppedConnections,omitempty"`
	SignalizedJunctions  []string `json:"signalizedJunctions,omitempty"` // signal-type junctions WITHOUT a usable program (unmodeled)
	SignalPrograms       int      `json:"signalPrograms,omitempty"`      // fixed-time programs compiled (ADR-0011)
	SignalLinks          int      `json:"signalLinks,omitempty"`         // internal lanes bound to a program link
	GuessedWidthLanes    []string `json:"guessedWidthLanes,omitempty"`
	GuessedInternalLanes []string `json:"guessedInternalLanes,omitempty"` // netconvert-generated junction interiors
	YieldApproaches      int      `json:"yieldApproaches,omitempty"`      // minor-class internal lanes (ADR-0010)
	StopApproaches       int      `json:"stopApproaches,omitempty"`       // stop-class internal lanes (ADR-0010)
	ConflictPairs        int      `json:"conflictPairs,omitempty"`        // crossing+merge foe pairs compiled (ADR-0010)
	Warnings             []string `json:"warnings,omitempty"`
}

// defaultLaneWidth is SUMO's own default lane width (m), applied when a lane
// carries no width attribute — and flagged as guessed.
const defaultLaneWidth = 3.2

// motorClasses are the SUMO vehicle classes our engine can carry; lanes
// restricted away from all of them (sidewalks, cycle tracks, rail) are
// skipped. An empty allow list means open to all (kept).
var motorClasses = map[string]bool{
	"passenger": true, "private": true, "bus": true, "coach": true,
	"delivery": true, "truck": true, "trailer": true, "motorcycle": true,
	"moped": true, "emergency": true, "authority": true, "army": true,
	"taxi": true, "hov": true, "evehicle": true, "custom1": true, "custom2": true,
}

// --- .net.xml schema (the parts v1 reads) ---

type xmlNet struct {
	Location  xmlLocation   `xml:"location"`
	Edges     []xmlEdge     `xml:"edge"`
	Junctions []xmlJunction `xml:"junction"`
	Conns     []xmlConn     `xml:"connection"`
	TLLogics  []xmlTLL      `xml:"tlLogic"`
}

type xmlLocation struct {
	NetOffset     string `xml:"netOffset,attr"`
	ConvBoundary  string `xml:"convBoundary,attr"`
	OrigBoundary  string `xml:"origBoundary,attr"`
	ProjParameter string `xml:"projParameter,attr"`
}

type xmlEdge struct {
	ID       string    `xml:"id,attr"`
	From     string    `xml:"from,attr"`
	To       string    `xml:"to,attr"`
	Function string    `xml:"function,attr"`
	Lanes    []xmlLane `xml:"lane"`
}

type xmlLane struct {
	ID       string  `xml:"id,attr"`
	Index    int     `xml:"index,attr"`
	Speed    float64 `xml:"speed,attr"`
	Length   float64 `xml:"length,attr"`
	Width    float64 `xml:"width,attr"`
	Allow    string  `xml:"allow,attr"`
	Disallow string  `xml:"disallow,attr"`
	Shape    string  `xml:"shape,attr"`
}

type xmlJunction struct {
	ID    string `xml:"id,attr"`
	Type  string `xml:"type,attr"`
	Shape string `xml:"shape,attr"`
}

type xmlConn struct {
	From      string `xml:"from,attr"`
	To        string `xml:"to,attr"`
	FromLane  int    `xml:"fromLane,attr"`
	ToLane    int    `xml:"toLane,attr"`
	Via       string `xml:"via,attr"`
	Dir       string `xml:"dir,attr"`
	State     string `xml:"state,attr"`
	TL        string `xml:"tl,attr"`
	LinkIndex int    `xml:"linkIndex,attr"`
}

// xmlTLL is a SUMO tlLogic element. v1 models only type="static" (fixed-time);
// actuated/other programs are reported and their junctions stay unmodeled.
type xmlTLL struct {
	ID        string     `xml:"id,attr"`
	Type      string     `xml:"type,attr"`
	ProgramID string     `xml:"programID,attr"`
	Offset    float64    `xml:"offset,attr"`
	Phases    []xmlPhase `xml:"phase"`
}

type xmlPhase struct {
	Duration float64 `xml:"duration,attr"`
	State    string  `xml:"state,attr"`
}

// Convert parses .net.xml bytes and compiles the NetFile plus the import
// report. Lane order follows the file: all lanes of each normal edge in
// document order, then all internal edges (SUMO writes them after the
// normal ones, but the sort here makes that guaranteed, not assumed).
func Convert(data []byte, opts Options) (*engine.NetFile, *Report, error) {
	var xn xmlNet
	if err := xml.Unmarshal(data, &xn); err != nil {
		return nil, nil, fmt.Errorf("netimport: parsing .net.xml: %w", err)
	}
	rep := &Report{}

	// Sort edges: normal before internal, document order within each class
	// (xml unmarshal preserves it; sort.SliceStable only reorders by class).
	edges := append([]xmlEdge(nil), xn.Edges...)
	sort.SliceStable(edges, func(i, j int) bool {
		return (edges[i].Function == "internal") == false && (edges[j].Function == "internal") == true
	})

	nf := &engine.NetFile{Version: 1, Name: opts.Name}
	if nf.Name == "" {
		nf.Name = opts.SourceFile
	}
	nf.Provenance = &engine.NetProvenance{
		Source:     opts.Source,
		SourceFile: opts.SourceFile,
		Imported:   opts.Imported,
		OSMBbox:    opts.OSMBbox,
		Projection: xn.Location.ProjParameter,
	}
	if off := parseXY(xn.Location.NetOffset); off != nil {
		nf.Provenance.NetOffset = *off
	}

	// Lane pass: build NetLanes, remember SUMO lane id → our id.
	ourID := map[string]string{}        // SUMO lane id (e.g. "E0_0", ":J1_0_0") → durable id
	laneJunction := map[string]string{} // our internal lane id → junction id
	used := map[string]bool{}
	for _, e := range edges {
		internal := e.Function == "internal"
		if len(e.Lanes) > 0 && allLanesNonMotor(e) {
			rep.SkippedEdges = append(rep.SkippedEdges, e.ID)
			for _, ln := range e.Lanes {
				rep.SkippedLanes = append(rep.SkippedLanes, ln.ID)
			}
			continue
		}
		kept := 0
		for _, ln := range e.Lanes {
			if !motorLane(ln) {
				rep.SkippedLanes = append(rep.SkippedLanes, ln.ID)
				continue
			}
			kept++
			shape, err := parseShape(ln.Shape)
			if err != nil {
				return nil, nil, fmt.Errorf("netimport: lane %s: %w", ln.ID, err)
			}
			if len(shape) < 2 {
				return nil, nil, fmt.Errorf("netimport: lane %s: shape has %d points (need >= 2)", ln.ID, len(shape))
			}
			width, guessed := ln.Width, false
			if width <= 0 {
				width, guessed = defaultLaneWidth, true
				rep.GuessedWidthLanes = append(rep.GuessedWidthLanes, ln.ID)
			}
			id := durableID(e.ID, ln.Index, internal, used)
			ourID[ln.ID] = id
			src := &engine.LaneSource{Edge: e.ID, Lane: ln.Index}
			if guessed {
				src.Guessed = append(src.Guessed, "width")
			}
			if internal {
				// Internal lanes are netconvert-generated junction
				// interiors — no OSM element underlies them (ADR-0009 §3).
				src.Guessed = append(src.Guessed, "internal-geometry")
				rep.GuessedInternalLanes = append(rep.GuessedInternalLanes, ln.ID)
			}
			nl := engine.NetLane{
				ID:         id,
				Section:    sectionOf(e, internal),
				Edge:       e.ID,
				EdgeIndex:  ln.Index,
				Length:     ln.Length,
				SpeedLimit: ln.Speed,
				Width:      width,
				Shape:      shape,
				Internal:   internal,
				Source:     src,
			}
			if internal {
				nl.Edge = "" // junction interiors never chain laterally
				laneJunction[id] = junctionOf(e.ID)
				rep.InternalLanes++
			} else {
				rep.Lanes++
			}
			nf.Lanes = append(nf.Lanes, nl)
		}
		if len(e.Lanes) > 0 && kept == 0 && !allLanesNonMotor(e) {
			return nil, nil, fmt.Errorf("netimport: edge %s: no lanes parsed", e.ID)
		}
	}
	if len(nf.Lanes) == 0 {
		return nil, nil, fmt.Errorf("netimport: no usable lanes in input")
	}

	// Connection pass: wire successors through the internal (via) lanes.
	// Per-lane successor ORDER is the engine's left-to-right convention
	// (pickSuccessor/driver turnChoice: first = leftmost): candidates are
	// ranked by the connection's dir attribute (t < l < L < s < R < r,
	// right-hand traffic — ADR-0009 §6's left-hand flag is future work),
	// ties keep document order.
	laneIdx := map[string]int{} // our id → position in nf.Lanes
	for i := range nf.Lanes {
		laneIdx[nf.Lanes[i].ID] = i
	}
	viaState := map[string]string{} // our internal lane id → connection state (M/m/s/w/...)
	viaTL := map[string]string{}    // our internal lane id → tlLogic program id (signal-controlled)
	viaLink := map[string]int{}     // our internal lane id → linkIndex into the program's state strings
	type succCand struct {
		to   string
		rank int
	}
	pending := map[string][]succCand{}
	addSucc := func(fromOur, toOur string, rank int) {
		for _, c := range pending[fromOur] {
			if c.to == toOur {
				return
			}
		}
		pending[fromOur] = append(pending[fromOur], succCand{to: toOur, rank: rank})
	}
	for _, c := range xn.Conns {
		fromLane, ok1 := ourID[fmt.Sprintf("%s_%d", c.From, c.FromLane)]
		toLane, ok2 := ourID[fmt.Sprintf("%s_%d", c.To, c.ToLane)]
		if !ok1 || !ok2 {
			rep.DroppedConnections = append(rep.DroppedConnections,
				fmt.Sprintf("%s_%d→%s_%d (endpoint lane skipped)", c.From, c.FromLane, c.To, c.ToLane))
			continue
		}
		if c.Via == "" {
			addSucc(fromLane, toLane, dirRank(c.Dir))
			rep.Connections++
			continue
		}
		via, ok := ourID[c.Via]
		if !ok {
			rep.DroppedConnections = append(rep.DroppedConnections,
				fmt.Sprintf("%s→%s via %s (internal lane missing)", fromLane, toLane, c.Via))
			continue
		}
		viaState[via] = c.State
		if c.TL != "" {
			viaTL[via] = c.TL
			viaLink[via] = c.LinkIndex
		}
		addSucc(fromLane, via, dirRank(c.Dir))
		addSucc(via, toLane, 0) // internal lanes have exactly one continuation
		rep.Connections++
	}
	for from, cands := range pending {
		sort.SliceStable(cands, func(i, j int) bool { return cands[i].rank < cands[j].rank })
		for _, c := range cands {
			nf.Lanes[laneIdx[from]].Successors = append(nf.Lanes[laneIdx[from]].Successors, c.to)
		}
	}

	// Origins and exits fall out of the wired graph: no predecessors =
	// demand portal, no successors = map edge.
	preds := map[string]int{}
	for i := range nf.Lanes {
		for _, s := range nf.Lanes[i].Successors {
			preds[s]++
		}
	}
	for i := range nf.Lanes {
		if preds[nf.Lanes[i].ID] == 0 {
			nf.Lanes[i].Origin = true
			rep.Origins++
		}
		if len(nf.Lanes[i].Successors) == 0 {
			nf.Lanes[i].Exit = true
			rep.Exits++
		}
	}

	// Junctions: signalized ones get fixed-time programs below when the
	// .net.xml carries a usable (static) tlLogic; the rest stay unmodeled
	// and are listed in the report.
	jtype := map[string]string{}
	for _, j := range xn.Junctions {
		jtype[j.ID] = j.Type
	}

	// Right-of-way pass (ADR-0010): annotate each internal lane with its
	// junction, its approach class (from the connection state), and its
	// conflict foes — internal lanes of the same junction whose paths cross
	// (shape polylines properly intersect) or merge (share the successor
	// lane; the junction-exit funnels where overlaps were observed). Merge
	// takes precedence when both hold. Signal-controlled and state-less
	// approaches carry no row class: their entry is governed by the light
	// (ADR-0011), with the off/blinking fallback to free traversal.
	byJunction := map[string][]int{} // junction id → internal lane positions, file order
	for i := range nf.Lanes {
		nl := &nf.Lanes[i]
		if !nl.Internal {
			continue
		}
		jid := laneJunction[nl.ID]
		nl.Junction = jid
		nl.Row = rowClass(viaState[nl.ID], jtype[jid], viaTL[nl.ID] != "")
		switch nl.Row {
		case "minor":
			rep.YieldApproaches++
		case "stop":
			rep.StopApproaches++
		}
		byJunction[jid] = append(byJunction[jid], i)
	}
	for _, idxs := range byJunction {
		for a := 0; a < len(idxs); a++ {
			for b := a + 1; b < len(idxs); b++ {
				la, lb := &nf.Lanes[idxs[a]], &nf.Lanes[idxs[b]]
				merge := len(la.Successors) > 0 && len(lb.Successors) > 0 &&
					la.Successors[0] == lb.Successors[0]
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
	// Signal pass (ADR-0011): compile static tlLogic elements into
	// fixed-time programs and bind each tl-referencing internal lane to its
	// (program, linkIndex). Only programs with at least one bound lane are
	// emitted. Non-static programs, missing tlLogic elements, and
	// out-of-range link indices leave the approach unsignalized (the
	// pre-signal semantics) and are reported.
	tllByID := map[string]*xmlTLL{}
	for i := range xn.TLLogics {
		tllByID[xn.TLLogics[i].ID] = &xn.TLLogics[i]
	}
	usable := map[string]bool{} // program id → static with phases
	for i := range xn.TLLogics {
		tll := &xn.TLLogics[i]
		switch {
		case tll.Type != "static":
			rep.Warnings = append(rep.Warnings,
				fmt.Sprintf("tlLogic %s: type %q unsupported (fixed-time static only); approaches stay unsignalized", tll.ID, tll.Type))
		case len(tll.Phases) == 0:
			rep.Warnings = append(rep.Warnings,
				fmt.Sprintf("tlLogic %s: no phases; approaches stay unsignalized", tll.ID))
		default:
			usable[tll.ID] = true
		}
	}
	usedPrograms := map[string]bool{}    // program id → at least one bound lane
	servedJunctions := map[string]bool{} // junctions with ≥1 bound lane
	missingTLL := map[string]bool{}      // tl ids referenced but not defined (warn once)
	for i := range nf.Lanes {
		nl := &nf.Lanes[i]
		if !nl.Internal {
			continue
		}
		tl, bound := viaTL[nl.ID]
		if !bound {
			continue
		}
		tll, defined := tllByID[tl]
		if !defined {
			if !missingTLL[tl] {
				missingTLL[tl] = true
				rep.Warnings = append(rep.Warnings,
					fmt.Sprintf("connections reference tlLogic %s but none is defined; approaches stay unsignalized", tl))
			}
			continue
		}
		if !usable[tl] {
			continue // warned above
		}
		link := viaLink[nl.ID]
		if link < 0 || link >= len(tll.Phases[0].State) {
			rep.Warnings = append(rep.Warnings,
				fmt.Sprintf("lane %s: linkIndex %d out of range for tlLogic %s (%d links); approach stays unsignalized",
					nl.ID, link, tl, len(tll.Phases[0].State)))
			continue
		}
		nl.TL = tl
		nl.TLLink = new(int)
		*nl.TLLink = link
		usedPrograms[tl] = true
		servedJunctions[nl.Junction] = true
		rep.SignalLinks++
	}
	for i := range xn.TLLogics {
		tll := &xn.TLLogics[i]
		if !usedPrograms[tll.ID] {
			continue
		}
		phases := make([]engine.NetSignalPhase, len(tll.Phases))
		for j, ph := range tll.Phases {
			phases[j] = engine.NetSignalPhase{Duration: ph.Duration, State: ph.State}
		}
		nf.Signals = append(nf.Signals, engine.NetSignal{
			ID:       tll.ID,
			Junction: tll.ID, // SUMO names a single-junction program after its junction
			Offset:   tll.Offset,
			Phases:   phases,
		})
		rep.SignalPrograms++
	}

	// Signalized junctions WITHOUT a usable program stay unmodeled — the
	// report is where a reviewer learns their traversal is unmodeled.
	for _, j := range xn.Junctions {
		if strings.Contains(j.Type, "traffic_light") && !servedJunctions[j.ID] {
			rep.SignalizedJunctions = append(rep.SignalizedJunctions, j.ID)
		}
	}
	if len(rep.SkippedEdges) > 0 {
		rep.Warnings = append(rep.Warnings, "non-motor edges skipped: "+strings.Join(rep.SkippedEdges, ", "))
	}
	if len(rep.SignalizedJunctions) > 0 {
		rep.Warnings = append(rep.Warnings,
			fmt.Sprintf("%d signalized junction(s) WITHOUT a usable fixed-time program — traversed unmodeled (free traversal): %s",
				len(rep.SignalizedJunctions), strings.Join(rep.SignalizedJunctions, ", ")))
	}
	return nf, rep, nil
}

// durableID assigns our stable lane ID: n<edge>_<lane> for normal lanes,
// i<edge>_<lane> for junction-internal ones, sanitized to [A-Za-z0-9_].
// Collisions after sanitizing get a deterministic _d<N> suffix (document
// order is fixed, so re-imports of the same file are stable).
func durableID(edgeID string, laneIdx int, internal bool, used map[string]bool) string {
	s := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, edgeID)
	s = strings.Trim(s, "_")
	if s == "" {
		s = "x"
	}
	prefix := "n"
	if internal {
		prefix = "i"
	}
	id := prefix + s + "_" + strconv.Itoa(laneIdx)
	for k := 2; used[id]; k++ {
		id = fmt.Sprintf("%s%s_%d_d%d", prefix, s, laneIdx, k)
	}
	used[id] = true
	return id
}

// dirRank ranks a connection's dir attribute for the left-to-right
// successor convention (first = leftmost; right-hand traffic). Unknown or
// missing dirs rank as straight.
func dirRank(dir string) int {
	switch dir {
	case "t": // turnaround (leftmost extreme)
		return 0
	case "l":
		return 1
	case "L": // partially left (merge-like)
		return 2
	case "R": // partially right
		return 4
	case "r":
		return 5
	default: // "s", "", unknown
		return 3
	}
}

// sectionOf names the metrics grouping: the raw edge id for normal lanes,
// "j:<junction>" for internal lanes (the internal edge id is ":<junction>_<n>").
func sectionOf(e xmlEdge, internal bool) string {
	if !internal {
		return e.ID
	}
	s := strings.TrimPrefix(e.ID, ":")
	if k := strings.LastIndex(s, "_"); k > 0 {
		s = s[:k]
	}
	return "j:" + s
}

// junctionOf extracts the junction id from an internal edge id
// (":<junction>_<n>"); mirrors sectionOf.
func junctionOf(internalEdgeID string) string {
	s := strings.TrimPrefix(internalEdgeID, ":")
	if k := strings.LastIndex(s, "_"); k > 0 {
		s = s[:k]
	}
	return s
}

// rowClass maps a SUMO connection state (plus its junction type and signal
// binding) to our approach class. Signal-controlled (tl) and signalized or
// state-less approaches stay unmodeled ("" → free traversal, ADR-0010);
// SUMO's "=" (equal priority) is modeled conservatively as minor, "w"
// (allway-stop) as a plain stop.
func rowClass(state, jtype string, tl bool) string {
	if tl || strings.Contains(jtype, "traffic_light") {
		return ""
	}
	switch state {
	case "M":
		return "major"
	case "m", "=":
		return "minor"
	case "s", "w":
		return "stop"
	}
	return ""
}

// polylinesCross reports whether two lane shape polylines properly cross —
// any segment pair intersecting in both interiors. Shared endpoints (fork
// starts, merge ends) do not count; merges are detected via successors.
func polylinesCross(a, b [][2]float64) bool {
	for i := 0; i+1 < len(a); i++ {
		for j := 0; j+1 < len(b); j++ {
			if segCross(a[i], a[i+1], b[j], b[j+1]) {
				return true
			}
		}
	}
	return false
}

// segCross reports whether segments ab and cd properly cross: c,d strictly
// on opposite sides of ab and a,b strictly on opposite sides of cd.
func segCross(a, b, c, d [2]float64) bool {
	x := func(o, p, q [2]float64) float64 {
		return (p[0]-o[0])*(q[1]-o[1]) - (p[1]-o[1])*(q[0]-o[0])
	}
	d1, d2 := x(a, b, c), x(a, b, d)
	d3, d4 := x(c, d, a), x(c, d, b)
	return (d1 > 0) != (d2 > 0) && d1 != 0 && d2 != 0 &&
		(d3 > 0) != (d4 > 0) && d3 != 0 && d4 != 0
}

// motorLane reports whether a lane is open to any motor vehicle class.
// SUMO's allow is a whitelist (empty = all); disallow is a blacklist.
func motorLane(ln xmlLane) bool {
	if ln.Allow != "" {
		for _, c := range strings.Fields(ln.Allow) {
			if motorClasses[c] {
				return true
			}
		}
		return false
	}
	if ln.Disallow != "" {
		blocked := map[string]bool{}
		for _, c := range strings.Fields(ln.Disallow) {
			blocked[c] = true
		}
		if blocked["all"] {
			return false
		}
		for c := range motorClasses {
			if !blocked[c] {
				return true
			}
		}
		return false
	}
	return true
}

func allLanesNonMotor(e xmlEdge) bool {
	for _, ln := range e.Lanes {
		if motorLane(ln) {
			return false
		}
	}
	return true
}

// parseShape parses a SUMO shape attribute: "x,y x,y ..." (meters in the
// network's local frame).
func parseShape(s string) ([][2]float64, error) {
	fields := strings.Fields(s)
	pts := make([][2]float64, 0, len(fields))
	for _, f := range fields {
		p := strings.Split(f, ",")
		if len(p) != 2 {
			return nil, fmt.Errorf("bad shape point %q", f)
		}
		x, err1 := strconv.ParseFloat(p[0], 64)
		y, err2 := strconv.ParseFloat(p[1], 64)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("bad shape point %q", f)
		}
		pts = append(pts, [2]float64{x, y})
	}
	return pts, nil
}

func parseXY(s string) *[2]float64 {
	p := strings.Split(s, ",")
	if len(p) != 2 {
		return nil
	}
	x, err1 := strconv.ParseFloat(p[0], 64)
	y, err2 := strconv.ParseFloat(p[1], 64)
	if err1 != nil || err2 != nil {
		return nil
	}
	return &[2]float64{x, y}
}
