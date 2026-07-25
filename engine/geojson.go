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
// junction/row are the junction right-of-way annotation (ADR-0010,
// internal lanes only): the viz clusters row="stop" lanes per junction
// approach to place stop signs (stopsign.ts).
type GeoJSONLaneProperties struct {
	ID         string  `json:"id"`
	SpeedLimit float64 `json:"speedLimit"` // m/s
	Width      float64 `json:"width"`      // m
	Internal   bool    `json:"internal"`
	Edge       string  `json:"edge,omitempty"`
	EdgeIndex  int     `json:"edgeIndex"`
	Junction   string  `json:"junction,omitempty"` // junction this internal lane belongs to
	Row        string  `json:"row,omitempty"`      // approach class: "major"|"minor"|"stop"
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
	return encodeGeoJSON(nf, w, geoJSONFeatures(nf, 0, len(nf.Lanes)))
}

// WriteGeoJSONRange writes lanes [start, end) as one standalone
// FeatureCollection — the same "frame" descriptor and the same per-feature
// encoding as WriteGeoJSON, so a range covering every lane is
// byte-identical to the single-file output (pinned in geojson_test.go).
// This is the PART-document encoding for demosrv's chunked network cache:
// oversized networks are split at lane (feature) boundaries, never by
// text-splitting JSON.
func WriteGeoJSONRange(nf *NetFile, w io.Writer, start, end int) error {
	if start < 0 {
		start = 0
	}
	if end < 0 {
		end = 0 // a negative end is empty, never "to the last lane"
	}
	if end > len(nf.Lanes) {
		end = len(nf.Lanes)
	}
	if start > end {
		start = end
	}
	return encodeGeoJSON(nf, w, geoJSONFeatures(nf, start, end))
}

// geoJSONFeatures builds the per-lane feature blocks for lanes [start, end)
// — the single marshaling path shared by the full document and parts.
func geoJSONFeatures(nf *NetFile, start, end int) []geoJSONFeature {
	var features []geoJSONFeature
	for i := start; i < end; i++ {
		nl := &nf.Lanes[i]
		width := nl.Width
		if width == 0 {
			width = 3.5 // the loader's default; keep the file and engine in agreement
		}
		features = append(features, geoJSONFeature{
			Type: "Feature",
			ID:   nl.ID,
			Properties: GeoJSONLaneProperties{
				ID:         nl.ID,
				SpeedLimit: nl.SpeedLimit,
				Width:      width,
				Internal:   nl.Internal,
				Edge:       nl.Edge,
				EdgeIndex:  nl.EdgeIndex,
				Junction:   nl.Junction,
				Row:        nl.Row,
			},
			Geometry: geoJSONGeometry{Type: "LineString", Coordinates: nl.Shape},
		})
	}
	return features
}

func encodeGeoJSON(nf *NetFile, w io.Writer, features []geoJSONFeature) error {
	if nf.Version != 1 {
		return fmt.Errorf("geojson: unsupported network version %d (want 1)", nf.Version)
	}
	coll := geoJSONCollection{Type: "FeatureCollection", Features: features}
	if p := nf.Provenance; p != nil {
		coll.Frame = &GeoJSONFrame{
			Projection: p.Projection,
			NetOffset:  p.NetOffset,
			OSMBbox:    p.OSMBbox,
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(coll); err != nil {
		return fmt.Errorf("geojson: %w", err)
	}
	return nil
}
