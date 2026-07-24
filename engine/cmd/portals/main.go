// Command portals inventories a compiled network's demand portals (origin
// and exit lanes) for scenario demand work: which lanes inject/drain
// traffic, how big/fast they are, and — when the source .net.xml is given —
// the OSM road class of each. The import report carries only counts; this
// is the per-lane inventory the demand napkin math weights.
//
// Usage:
//
//	portals -net region.json [-netxml region.net.xml] [-out portals.json]
//
// Fragment lanes (clipped stubs and topology defects that also satisfy
// "no predecessors") are flagged, not removed: length under -fraglen.
package main

import (
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"os"
	"strings"

	engine "traffic-sim/engine"
)

type portal struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"` // "origin" | "exit"
	Edge       string     `json:"edge"`
	Class      string     `json:"class,omitempty"` // OSM class via .net.xml edge type, e.g. "primary"
	Length     float64    `json:"length"`
	SpeedLimit float64    `json:"speedLimit"` // m/s
	Width      float64    `json:"width"`
	Fragment   bool       `json:"fragment,omitempty"` // short stub, likely a clipping artifact
	Start      [2]float64 `json:"start"`              // local metric frame (see provenance)
	End        [2]float64 `json:"end"`
}

type netXMLEdge struct {
	ID   string `xml:"id,attr"`
	Type string `xml:"type,attr"`
}

type netXML struct {
	Edges []netXMLEdge `xml:"edge"`
}

func main() {
	netPath := flag.String("net", "", "compiled network JSON (required)")
	xmlPath := flag.String("netxml", "", "source .net.xml for road classes (optional)")
	outPath := flag.String("out", "", "output path (default stdout)")
	fragLen := flag.Float64("fraglen", 30, "length under which a portal is flagged a fragment (m)")
	flag.Parse()
	if *netPath == "" {
		fmt.Fprintln(os.Stderr, "portals: -net is required")
		os.Exit(2)
	}

	data, err := os.ReadFile(*netPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "portals:", err)
		os.Exit(1)
	}
	var nf engine.NetFile
	if err := json.Unmarshal(data, &nf); err != nil {
		fmt.Fprintln(os.Stderr, "portals: parse:", err)
		os.Exit(1)
	}

	classByEdge := map[string]string{}
	if *xmlPath != "" {
		xdata, err := os.ReadFile(*xmlPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "portals:", err)
			os.Exit(1)
		}
		var nx netXML
		if err := xml.Unmarshal(xdata, &nx); err != nil {
			fmt.Fprintln(os.Stderr, "portals: netxml parse:", err)
			os.Exit(1)
		}
		for _, e := range nx.Edges {
			if i := strings.LastIndex(e.Type, "."); i >= 0 {
				classByEdge[e.ID] = e.Type[i+1:]
			}
		}
	}

	var origins, exits []portal
	for _, l := range nf.Lanes {
		if !l.Origin && !l.Exit {
			continue
		}
		kind := "origin"
		if l.Exit {
			kind = "exit"
		}
		p := portal{
			ID: l.ID, Kind: kind, Edge: l.Section,
			Class:  classByEdge[l.Section],
			Length: l.Length, SpeedLimit: l.SpeedLimit, Width: l.Width,
			Fragment: l.Length < *fragLen,
		}
		if len(l.Shape) > 0 {
			p.Start = l.Shape[0]
			p.End = l.Shape[len(l.Shape)-1]
		}
		if l.Origin {
			origins = append(origins, p)
		} else {
			exits = append(exits, p)
		}
	}

	doc := map[string]any{
		"network": nf.Name,
		"origins": origins,
		"exits":   exits,
		"summary": map[string]int{
			"origins": len(origins), "exits": len(exits),
		},
	}
	enc := json.NewEncoder(os.Stdout)
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "portals:", err)
			os.Exit(1)
		}
		defer f.Close()
		enc = json.NewEncoder(f)
	}
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		fmt.Fprintln(os.Stderr, "portals: encode:", err)
		os.Exit(1)
	}
}
