// netimport is the netconvert bootstrap CLI (ADR-0009 §1): it compiles a
// SUMO .net.xml file into the compiled network JSON (format v1,
// contracts/network-format-v1.md) and prints the import report.
//
// Bootstrap (see contracts/network-format-v1.md for the full recipe):
//
//	netconvert --osm-files region.osm -o region.net.xml --proj.utm
//	netimport -in region.net.xml -out region.json -bbox "S,W,N,E"
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"traffic-sim/engine/netimport"
)

func main() {
	in := flag.String("in", "", "input .net.xml file (netconvert output)")
	out := flag.String("out", "", "output network JSON (format v1)")
	name := flag.String("name", "", "network name (default: input file name)")
	source := flag.String("source", "", "provenance stamp, e.g. \"netimport (netconvert 1.27.1 .net.xml)\"")
	bbox := flag.String("bbox", "", "OSM extract bbox \"S,W,N,E\" (provenance)")
	report := flag.String("report", "", "optional path for the JSON import report")
	flag.Parse()
	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: netimport -in file.net.xml -out net.json [-name n] [-source s] [-bbox S,W,N,E] [-report report.json]")
		os.Exit(2)
	}
	data, err := os.ReadFile(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "netimport:", err)
		os.Exit(1)
	}
	src := *source
	if src == "" {
		src = "netimport (.net.xml)"
	}
	nf, rep, err := netimport.Convert(data, netimport.Options{
		Name:       *name,
		SourceFile: *in,
		Source:     src,
		Imported:   time.Now().UTC().Format(time.RFC3339),
		OSMBbox:    *bbox,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "netimport:", err)
		os.Exit(1)
	}
	js, err := json.MarshalIndent(nf, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "netimport: marshal:", err)
		os.Exit(1)
	}
	js = append(js, '\n')
	if err := os.WriteFile(*out, js, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "netimport:", err)
		os.Exit(1)
	}
	fmt.Printf("netimport: %s → %s: %d lanes (%d internal), %d connections, %d origins, %d exits\n",
		*in, *out, rep.Lanes, rep.InternalLanes, rep.Connections, rep.Origins, rep.Exits)
	for _, w := range rep.Warnings {
		fmt.Println("netimport: warning:", w)
	}
	if *report != "" {
		rj, err := json.MarshalIndent(rep, "", "  ")
		if err == nil {
			err = os.WriteFile(*report, append(rj, '\n'), 0o644)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "netimport: report:", err)
			os.Exit(1)
		}
	}
}
