// bake bakes a recorded run into the static replay artifacts of ADR-0023:
// a strict, CRC-verified re-simulation of the record plane (natsio.BakeSource
// — any CRC/verb divergence ABORTS, never logs-and-continues) written out as
// brotli-precompressed TSRB v1 vehicle-frame chunks and TSRL v1 lane-speed
// chunks, spatially partitioned into z11 web-mercator regions and
// time-windowed (60 s), plus the TSSG signal table, the occupied-lane id
// table, the network artifact (GeoJSON for small networks, PMTiles via the
// pinned EXTERNAL tippecanoe binary for city scale), and the index.json
// manifest — all under one content-addressed prefix:
//
//	bake -store DIR -run RUN -out OUTDIR [-overlays DIR] [-net-format auto|geojson|pmtiles]
//
// STORE EXCLUSIVITY: exactly one broker may open a JetStream store dir at a
// time — the serve that recorded the run must have EXITED before baking.
package main

import (
	"flag"
	"fmt"
	"os"

	"traffic-sim/engine/natsio"
)

func main() {
	store := flag.String("store", "", "durable JetStream store dir written by serve -store (required); the recording serve must have exited first — one broker per store dir")
	run := flag.String("run", "", "recorded run id to bake (required)")
	out := flag.String("out", "", "output root (required); the bake lands at {out}/baked/{run}/{hash12}/ and city PMTiles at {out}/city/{hash12}/")
	overlays := flag.String("overlays", "", "optional demo overlay dir (the files demosrv serves at /overlay/*); copied into the prefix and listed in index.json, and hashed into the content key")
	netFormat := flag.String("net-format", "auto", "network artifact: geojson | pmtiles | auto (pmtiles above the city-scale lane threshold, needs the pinned tippecanoe binary)")
	baseURL := flag.String("base-url", "https://data.phantomjam.com", "deployment origin used for the absolute network.pmtiles URL in index.json")
	flag.Parse()

	if *store == "" || *run == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "bake: -store, -run, and -out are required")
		os.Exit(2)
	}

	js, shutdown, err := natsio.OpenRecordingStore(*store)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bake:", err)
		os.Exit(1)
	}
	defer shutdown()

	src, err := natsio.NewBakeSource(js, *run)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bake:", err)
		os.Exit(1)
	}

	res, err := bake(src, bakeParams{
		OutDir:      *out,
		OverlaysDir: *overlays,
		NetFormat:   *netFormat,
		BaseURL:     *baseURL,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "bake:", err)
		os.Exit(1)
	}

	idx := res.Index
	var chunkBytes int64
	regions := idx.Regions
	for _, r := range regions {
		for _, c := range r.Frames {
			chunkBytes += int64(c.Bytes)
		}
		for _, c := range r.Lanes {
			chunkBytes += int64(c.Bytes)
		}
	}
	fmt.Printf("bake: run %q → %s\n", idx.Run, res.Prefix)
	fmt.Printf("bake: ticks [%d, %d] dt %g, %d regions, %d chunk bytes (.br), network: %s\n",
		idx.TickStart, idx.TickEnd, idx.Dt, len(regions), chunkBytes, networkSummary(idx))
}

func networkSummary(idx *bakeIndex) string {
	if idx.Network.PMTiles != "" {
		return idx.Network.PMTiles
	}
	return idx.Network.GeoJSON
}
