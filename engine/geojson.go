package engine

import (
	"encoding/json"
	"fmt"
	"io"
)

// geojson.go — export of a compiled network (format v1) as GeoJSON for the
// viz client (M6): one LineString feature per lane polyline. Coordinates
// stay in the network's LOCAL METRIC FRAME (the frame TSSF v1 snapshot
// frames also use, engine/natsio/frame.go); the file is not RFC 7946
// WGS84. The frame descriptor rides along as the FeatureCollection foreign
// member "frame" (projection + netOffset, copied from the network
// provenance) so the client can project to lng/lat — it must do that for
// live vehicle positions anyway. GeoJSON tooling expecting WGS84 will show
// nonsense coordinates; this file is an engine↔viz artifact.

// GeoJSONLaneProperties is the per-lane property block the viz styles on:
// id is the promoteId / feature-state key, speedLimit the congestion-ratio
// denominator, width the line width, internal marks junction interiors.
// edge/edgeIndex are the lateral-chaining group (network-format v1): the
// viz draws group-boundary casing from them so same-road lanes read as one
// road; empty edge (junction interiors) = no lateral neighbors.
type GeoJSONLaneProperties struct {
	ID         string  `json:"id"`
	SpeedLimit float64 `json:"speedLimit"` // m/s
	Width      float64 `json:"width"`      // m
	Internal   bool    `json:"internal"`
	Edge       string  `json:"edge,omitempty"`
	EdgeIndex  int     `json:"edgeIndex"`
}

// GeoJSONFrame is the foreign member describing the local metric frame the
// coordinates live in (network-format v1 provenance fields).
type GeoJSONFrame struct {
	Projection string     `json:"projection"`
	NetOffset  [2]float64 `json:"netOffset"`
	OSMBbox    string     `json:"osmBbox,omitempty"`
}

type geoJSONGeometry struct {
	Type        string       `json:"type"` // "LineString"
	Coordinates [][2]float64 `json:"coordinates"`
}

type geoJSONFeature struct {
	Type       string                `json:"type"` // "Feature"
	ID         string                `json:"id"`
	Properties GeoJSONLaneProperties `json:"properties"`
	Geometry   geoJSONGeometry       `json:"geometry"`
}

type geoJSONCollection struct {
	Type     string           `json:"type"` // "FeatureCollection"
	Frame    *GeoJSONFrame    `json:"frame,omitempty"`
	Features []geoJSONFeature `json:"features"`
}

// WriteGeoJSON writes nf as a GeoJSON FeatureCollection (one LineString per
// lane, local metric frame + "frame" descriptor). Lane order is preserved.
func WriteGeoJSON(nf *NetFile, w io.Writer) error {
	if nf.Version != 1 {
		return fmt.Errorf("geojson: unsupported network version %d (want 1)", nf.Version)
	}
	coll := geoJSONCollection{Type: "FeatureCollection"}
	if p := nf.Provenance; p != nil {
		coll.Frame = &GeoJSONFrame{
			Projection: p.Projection,
			NetOffset:  p.NetOffset,
			OSMBbox:    p.OSMBbox,
		}
	}
	for i := range nf.Lanes {
		nl := &nf.Lanes[i]
		width := nl.Width
		if width == 0 {
			width = 3.5 // the loader's default; keep the file and engine in agreement
		}
		coll.Features = append(coll.Features, geoJSONFeature{
			Type: "Feature",
			ID:   nl.ID,
			Properties: GeoJSONLaneProperties{
				ID:         nl.ID,
				SpeedLimit: nl.SpeedLimit,
				Width:      width,
				Internal:   nl.Internal,
				Edge:       nl.Edge,
				EdgeIndex:  nl.EdgeIndex,
			},
			Geometry: geoJSONGeometry{Type: "LineString", Coordinates: nl.Shape},
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(coll); err != nil {
		return fmt.Errorf("geojson: %w", err)
	}
	return nil
}
