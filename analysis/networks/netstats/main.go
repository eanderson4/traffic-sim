// Command netstats computes static GIS-style metrics over the compiled
// network-format v1 files in data/networks/. Read-only: it parses each
// <name>/<name>.json and prints a Markdown comparison table plus a JSON
// dump for the report.
//
// Usage: go run . -dir ../../../data/networks
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type lane struct {
	ID         string      `json:"id"`
	Section    string      `json:"section"`
	Edge       string      `json:"edge"`
	Length     float64     `json:"length"`
	SpeedLimit float64     `json:"speedLimit"`
	Shape      [][]float64 `json:"shape"`
	Origin     bool        `json:"origin"`
	Exit       bool        `json:"exit"`
	Internal   bool        `json:"internal"`
	Junction   string      `json:"junction"`
	Row        string      `json:"row"`
	TL         string      `json:"tl"`
}

type signal struct {
	ID       string `json:"id"`
	Junction string `json:"junction"`
}

type network struct {
	Name    string   `json:"name"`
	Lanes   []lane   `json:"lanes"`
	Signals []signal `json:"signals"`
}

type stats struct {
	Name          string      `json:"name"`
	Lanes         int         `json:"lanes"`
	InternalLanes int         `json:"internalLanes"`
	Edges         int         `json:"edges"`
	LaneKM        float64     `json:"laneKM"`
	InternalKM    float64     `json:"internalKM"`
	AreaKM2       float64     `json:"areaKM2"`
	LaneKMPerKM2  float64     `json:"laneKMPerKM2"`
	Junctions     int         `json:"junctions"`
	JuncPerKM2    float64     `json:"junctionsPerKM2"`
	SignalJunc    int         `json:"signalizedJunctions"`
	SignalShare   float64     `json:"signalShare"` // % of junctions with a compiled program
	StopApproach  int         `json:"stopApproaches"`
	YieldApproach int         `json:"yieldApproaches"` // row=minor
	Origins       int         `json:"origins"`
	Exits         int         `json:"exits"`
	OriginsPerKM2 float64     `json:"originsPerKM2"`
	AvgBlockM     float64     `json:"avgBlockM"` // mean edge length
	MedBlockM     float64     `json:"medBlockM"`
	WAvgBlockM    float64     `json:"wavgBlockM"` // length-weighted mean edge length (robust to micro-fragments)
	FragShare     float64     `json:"fragShare"`  // % of edges shorter than 5 m (junction-cluster splits)
	AvgLanesEdge  float64     `json:"avgLanesPerEdge"`
	LaneDist      map[int]int `json:"laneCountDist"` // lanes-per-edge histogram (5 = 5+)
	AvgSpeedKMH   float64     `json:"avgSpeedKMH"`   // length-weighted mean limit
	FwyShare      float64     `json:"fwyShare"`      // % of lane-km with limit >= 22 m/s (~80 km/h)
	CapProxy      float64     `json:"capProxy"`      // sum(length_km * limit_kmh) = lane-km*km/h
}

func analyze(n network) stats {
	s := stats{Name: n.Name, LaneDist: map[int]int{}}
	edges := map[string]int{} // edge -> lane count
	edgeLen := map[string]float64{}
	junctions := map[string]bool{}
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	var lenSum, spdSum, capSum float64
	var blockLens []float64

	for _, l := range n.Lanes {
		for _, p := range l.Shape {
			if p[0] < minX {
				minX = p[0]
			}
			if p[0] > maxX {
				maxX = p[0]
			}
			if p[1] < minY {
				minY = p[1]
			}
			if p[1] > maxY {
				maxY = p[1]
			}
		}
		if l.Internal {
			s.InternalLanes++
			s.InternalKM += l.Length / 1000
			j := l.Junction
			if j == "" && strings.HasPrefix(l.Section, "j:") {
				j = strings.TrimPrefix(l.Section, "j:")
			}
			if j != "" {
				junctions[j] = true
			}
			switch l.Row {
			case "stop":
				s.StopApproach++
			case "minor":
				s.YieldApproach++
			}
			continue
		}
		s.Lanes++
		s.LaneKM += l.Length / 1000
		spdSum += l.Length * l.SpeedLimit
		lenSum += l.Length
		capSum += (l.Length / 1000) * (l.SpeedLimit * 3.6)
		if l.SpeedLimit >= 22 {
			s.FwyShare += l.Length / 1000
		}
		if l.Origin {
			s.Origins++
		}
		if l.Exit {
			s.Exits++
		}
		edges[l.Edge]++
		edgeLen[l.Edge] = l.Length
	}

	s.Edges = len(edges)
	for _, c := range edges {
		if c > 5 {
			c = 5
		}
		s.LaneDist[c]++
	}
	for _, e := range edges {
		s.AvgLanesEdge += float64(e)
	}
	if s.Edges > 0 {
		s.AvgLanesEdge /= float64(s.Edges)
	}
	var l2, l1 float64
	var frags int
	edgeIDs := make([]string, 0, len(edgeLen))
	for id := range edgeLen {
		edgeIDs = append(edgeIDs, id)
	}
	sort.Strings(edgeIDs) // deterministic accumulation order for the float sums
	for _, id := range edgeIDs {
		ln := edgeLen[id]
		blockLens = append(blockLens, ln)
		s.AvgBlockM += ln
		l1 += ln
		l2 += ln * ln
		if ln < 5 {
			frags++
		}
	}
	if len(blockLens) > 0 {
		s.AvgBlockM /= float64(len(blockLens))
		sort.Float64s(blockLens)
		s.MedBlockM = blockLens[len(blockLens)/2]
		s.FragShare = 100 * float64(frags) / float64(len(blockLens))
	}
	if l1 > 0 {
		s.WAvgBlockM = l2 / l1
	}
	if lenSum > 0 {
		s.AvgSpeedKMH = spdSum / lenSum * 3.6
	}
	if s.LaneKM > 0 {
		s.FwyShare = 100 * s.FwyShare / s.LaneKM
	}
	s.CapProxy = capSum

	s.AreaKM2 = (maxX - minX) * (maxY - minY) / 1e6
	if s.AreaKM2 > 0 {
		s.LaneKMPerKM2 = s.LaneKM / s.AreaKM2
		s.OriginsPerKM2 = float64(s.Origins) / s.AreaKM2
	}
	s.Junctions = len(junctions)
	if s.AreaKM2 > 0 {
		s.JuncPerKM2 = float64(s.Junctions) / s.AreaKM2
	}
	sigJunc := map[string]bool{}
	for _, p := range n.Signals {
		j := p.Junction
		if j == "" {
			j = p.ID
		}
		sigJunc[j] = true
	}
	s.SignalJunc = len(sigJunc)
	if s.Junctions > 0 {
		s.SignalShare = 100 * float64(s.SignalJunc) / float64(s.Junctions)
	}
	return s
}

func main() {
	dir := flag.String("dir", "../../../data/networks", "networks dir")
	flag.Parse()

	files, err := filepath.Glob(filepath.Join(*dir, "*", "*.json"))
	if err != nil {
		panic(err)
	}
	var all []stats
	for _, f := range files {
		base := filepath.Base(f)
		if base == "import-report.json" || base == "portals.json" {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			panic(err)
		}
		var n network
		if err := json.Unmarshal(data, &n); err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", f, err)
			continue
		}
		if n.Name == "" {
			n.Name = strings.TrimSuffix(base, ".json")
		}
		all = append(all, analyze(n))
	}
	sort.Slice(all, func(i, j int) bool { return all[i].LaneKM > all[j].LaneKM })

	fmt.Println("| network | lanes | junc lanes | edges | lane-km | km² | lane-km/km² | juncs | junc/km² | signal % | yield appr | origins | exits | block m (avg/med/wavg) | frag % | lanes/edge | avg km/h | fwy % |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|")
	for _, s := range all {
		fmt.Printf("| %s | %d | %d | %d | %.1f | %.2f | %.1f | %d | %.1f | %.0f | %d | %d | %d | %.0f/%.0f/%.0f | %.0f | %.2f | %.0f | %.1f |\n",
			s.Name, s.Lanes, s.InternalLanes, s.Edges, s.LaneKM, s.AreaKM2,
			s.LaneKMPerKM2, s.Junctions, s.JuncPerKM2, s.SignalShare,
			s.YieldApproach, s.Origins, s.Exits,
			s.AvgBlockM, s.MedBlockM, s.WAvgBlockM, s.FragShare, s.AvgLanesEdge, s.AvgSpeedKMH, s.FwyShare)
	}
	fmt.Println()
	for _, s := range all {
		fmt.Printf("%s lane-count dist (1/2/3/4/5+): %d/%d/%d/%d/%d\n",
			s.Name, s.LaneDist[1], s.LaneDist[2], s.LaneDist[3], s.LaneDist[4], s.LaneDist[5])
	}

	enc := json.NewEncoder(os.Stderr)
	enc.SetIndent("", "  ")
	enc.Encode(all)
}
