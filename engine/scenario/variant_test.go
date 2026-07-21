package scenario

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"traffic-sim/engine"
)

// writeNetAlt emits a second minimal network whose origin lane is c_0, not
// a_0 — the whole-network replacement fixture.
func writeNetAlt(t *testing.T, dir, rel string) {
	t.Helper()
	nf := engine.NetFile{
		Version: 1,
		Name:    "test-alt",
		Lanes: []engine.NetLane{
			{ID: "c_0", Section: "c", Length: 500, SpeedLimit: 15, Width: 3.2,
				Shape: [][2]float64{{0, 0}, {500, 0}}, Successors: []string{"d_0"}, Origin: true},
			{ID: "d_0", Section: "d", Length: 500, SpeedLimit: 15, Width: 3.2,
				Shape: [][2]float64{{500, 0}, {1000, 0}}, Exit: true},
		},
	}
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, rel, string(data))
}

const variantDemand = `format_version: 1
flows:
  - id: wb-commute
    origin: a_0
    veh_per_h: 900
    spacing: poisson
    vtypes:
      car: 0.9
      truck: 0.1
`

// variantPair builds base/ and variants/x/ side by side under one temp
// root; the base carries one id-anchored flow.
func variantPair(t *testing.T, variantYAML string) (baseDir, variantDir string) {
	t.Helper()
	root := t.TempDir()
	baseDir = filepath.Join(root, "base")
	variantDir = filepath.Join(root, "variants", "x")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeNet(t, baseDir)
	writeFile(t, baseDir, ManifestFile, goodManifest)
	writeFile(t, baseDir, "demand/main.yaml", variantDemand)
	writeFile(t, variantDir, VariantManifest, variantYAML)
	return baseDir, variantDir
}

const identityVariant = `format_version: 1
id: test-baseline
base: ../../base
`

// A variant that overrides nothing and adds nothing materializes to the
// base itself — identical content hash (ADR-0012 M12 acceptance shape).
func TestVariantIdentityHash(t *testing.T) {
	baseDir, variantDir := variantPair(t, identityVariant)
	base, err := Load(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	v, err := Load(variantDir)
	if err != nil {
		t.Fatalf("Load variant: %v", err)
	}
	if v.Hash() != base.Hash() {
		t.Errorf("identity variant hash %s != base %s", v.Hash(), base.Hash())
	}
	if v.Manifest.ID != "test-baseline" || len(v.Demands) != 1 {
		t.Errorf("materialized: %+v demands %d", v.Manifest, len(v.Demands))
	}
}

const patchVariant = `format_version: 1
id: test-peak-x2
base: ../../base
seed: 99
patches:
  - part: demand/main.yaml
    flow: wb-commute
    set:
      veh_per_h: 1800
      vtypes:
        car: 0.8
`

// handWrittenPeak writes the hand-authored equivalent of patchVariant.
func handWrittenPeak(t *testing.T) string {
	t.Helper()
	hand := t.TempDir()
	writeNet(t, hand)
	writeFile(t, hand, ManifestFile, strings.Replace(goodManifest, "id: test-baseline", "id: test-peak-x2", 1))
	writeFile(t, hand, "demand/main.yaml", `format_version: 1
flows:
  - id: wb-commute
    origin: a_0
    veh_per_h: 1800
    spacing: poisson
    vtypes:
      car: 0.8
      truck: 0.1
`)
	return hand
}

// A patched variant hashes identically to the equivalent hand-written
// scenario: the overlay is authorship, not identity. Nested mapping merge
// keeps the unmentioned truck weight; seed is a run coordinate, not
// content — the variant's seed: 99 must not move the hash.
func TestVariantPatchEquivalence(t *testing.T) {
	_, variantDir := variantPair(t, patchVariant)
	v, err := Load(variantDir)
	if err != nil {
		t.Fatalf("Load variant: %v", err)
	}
	f := v.Demands[0].Flows[0]
	if f.VehPerH != 1800 {
		t.Errorf("veh_per_h = %v, want 1800 (patched)", f.VehPerH)
	}
	if f.VTypes["car"] != 0.8 || f.VTypes["truck"] != 0.1 {
		t.Errorf("vtypes = %v, want car 0.8 (patched) + truck 0.1 (merged)", f.VTypes)
	}
	if v.Manifest.Seed != 99 {
		t.Errorf("seed override = %d, want 99", v.Manifest.Seed)
	}

	h, err := Load(handWrittenPeak(t))
	if err != nil {
		t.Fatal(err)
	}
	if v.Hash() != h.Hash() {
		t.Errorf("patched variant hash %s != hand-written equivalent %s", v.Hash(), h.Hash())
	}

	// The materialized variant builds a run like any scenario.
	spec, err := v.RunSpec(map[string]*engine.VehicleType{"car": &engine.Car, "truck": &engine.Truck})
	if err != nil {
		t.Fatalf("RunSpec: %v", err)
	}
	if _, err := engine.NewEngine(spec); err != nil {
		t.Fatalf("materialized variant spec does not build an engine: %v", err)
	}
}

// Patched variant and hand-written equivalent are the same RUN, not just
// the same hash (M12 addendum §5 acceptance: the M11 CRC pattern). The
// seed override is a run coordinate — both runs use the variant's seed.
func TestVariantRunBitIdentity(t *testing.T) {
	_, variantDir := variantPair(t, patchVariant)
	v, err := Load(variantDir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Load(handWrittenPeak(t))
	if err != nil {
		t.Fatal(err)
	}
	reg := map[string]*engine.VehicleType{"car": &engine.Car, "truck": &engine.Truck}
	specs := [2]engine.RunSpec{}
	for i, s := range []*Scenario{v, h} {
		spec, err := s.RunSpec(reg)
		if err != nil {
			t.Fatal(err)
		}
		spec.Seed = v.Manifest.Seed // the override coordinate for both
		specs[i] = spec
	}
	ev, err := engine.NewEngine(specs[0])
	if err != nil {
		t.Fatal(err)
	}
	eh, err := engine.NewEngine(specs[1])
	if err != nil {
		t.Fatal(err)
	}
	for tick := uint64(1); tick <= 500; tick++ {
		ev.Step()
		eh.Step()
		if tick%100 == 0 && ev.CRC() != eh.CRC() {
			t.Fatalf("tick %d: variant crc %016x != hand-written crc %016x", tick, ev.CRC(), eh.CRC())
		}
	}
	if ev.CRC() != eh.CRC() {
		t.Fatalf("final crc %016x != %016x", ev.CRC(), eh.CRC())
	}
}

// The golden variant vector pins the overlay path of the hash protocol:
// same canary role as TestGoldenHash (a canonicalization change is a
// format event, never silent).
func TestGoldenVariantHash(t *testing.T) {
	_, variantDir := variantPair(t, patchVariant)
	v, err := Load(variantDir)
	if err != nil {
		t.Fatal(err)
	}
	const want = "642ec5aa0b61d24a9337eee98c4dc31d6023511b056b12db3861c365a494bad2"
	if v.Hash() != want {
		t.Fatalf("variant hash = %s, want golden %s", v.Hash(), want)
	}
}

// Sequences replace wholesale: a patched slices program is one entity —
// the base's slices do not survive a set that carries its own.
func TestVariantSequenceReplace(t *testing.T) {
	_, variantDir := variantPair(t, `format_version: 1
id: sliced
base: ../../base
patches:
  - part: demand/main.yaml
    flow: wb-commute
    set:
      veh_per_h: 0
      slices:
        - {start_s: 0, end_s: 100, veh_per_h: 300}
`)
	// veh_per_h alongside slices is dead config; the base flow has
	// veh_per_h: 900, so the patch must zero it to be valid — and setting
	// slices must not leave a stale rate behind. Zeroing via set is
	// scalar replacement, not deletion.
	v, err := Load(variantDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	f := v.Demands[0].Flows[0]
	if len(f.Slices) != 1 || f.Slices[0].VehPerH != 300 {
		t.Errorf("slices = %+v, want the patch's single slice only", f.Slices)
	}
	if f.VehPerH != 0 {
		t.Errorf("veh_per_h = %v, want 0 (replaced)", f.VehPerH)
	}
}

// The patch anchor is (part path, flow id): two base demand parts may both
// carry a flow with the same id — a patch names its part and touches only
// that part's flow (the M12 addendum's namespacing resolution).
func TestVariantAnchorNamespacing(t *testing.T) {
	root := t.TempDir()
	baseDir := filepath.Join(root, "base")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeNet(t, baseDir)
	writeFile(t, baseDir, ManifestFile, `format_version: 1
id: two-parts
seed: 1
ticks: 100
network: network.json
demand:
  - demand/a.yaml
  - demand/b.yaml
`)
	writeFile(t, baseDir, "demand/a.yaml", `format_version: 1
flows:
  - {id: x, origin: a_0, veh_per_h: 100, spacing: constant}
`)
	writeFile(t, baseDir, "demand/b.yaml", `format_version: 1
flows:
  - {id: x, origin: a_0, veh_per_h: 200, spacing: constant}
`)
	variantDir := filepath.Join(root, "v")
	writeFile(t, variantDir, VariantManifest, `format_version: 1
id: two-parts-patched
base: ../base
patches:
  - {part: demand/b.yaml, flow: x, set: {veh_per_h: 999}}
`)
	v, err := Load(variantDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if v.Demands[0].Flows[0].VehPerH != 100 {
		t.Errorf("part a flow = %v, want 100 (untouched)", v.Demands[0].Flows[0].VehPerH)
	}
	if v.Demands[1].Flows[0].VehPerH != 999 {
		t.Errorf("part b flow = %v, want 999 (patched)", v.Demands[1].Flows[0].VehPerH)
	}
}

// Added part files extend the base's demand; both parts validate against
// the effective network and share the run.
func TestVariantAddedParts(t *testing.T) {
	_, variantDir := variantPair(t, `format_version: 1
id: with-extra
base: ../../base
demand:
  - demand/extra.yaml
`)
	writeFile(t, variantDir, "demand/extra.yaml", `format_version: 1
flows:
  - origin: a_0
    veh_per_h: 120
    spacing: constant
`)
	v, err := Load(variantDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(v.Demands) != 2 || len(v.Manifest.Demand) != 2 {
		t.Fatalf("demands = %d parts %v", len(v.Demands), v.Manifest.Demand)
	}
	if v.Demands[1].Flows[0].VehPerH != 120 {
		t.Errorf("added flow rate = %v", v.Demands[1].Flows[0].VehPerH)
	}
}

// Whole-network replacement: demand re-validates against the EFFECTIVE
// network — a replacement whose origins don't cover the base demand fails
// at variant load, not at spawn time.
func TestVariantNetworkReplacement(t *testing.T) {
	baseDir, variantDir := variantPair(t, `format_version: 1
id: new-net
base: ../../base
network: net/alt.json
`)
	writeNetAlt(t, variantDir, "net/alt.json")
	if _, err := Load(variantDir); err == nil || !strings.Contains(err.Error(), "not a spawn origin") {
		t.Fatalf("base demand vs replaced network: err = %v", err)
	}
	// Same replacement, but the variant patches the flow onto an origin
	// that EXISTS in the new network — valid end to end.
	writeFile(t, variantDir, VariantManifest, `format_version: 1
id: new-net
base: ../../base
network: net/alt.json
patches:
  - part: demand/main.yaml
    flow: wb-commute
    set:
      origin: c_0
`)
	v, err := Load(variantDir)
	if err != nil {
		t.Fatalf("Load with patched origin: %v", err)
	}
	if v.Demands[0].Flows[0].Origin != "c_0" {
		t.Errorf("origin = %q, want c_0", v.Demands[0].Flows[0].Origin)
	}
	if v.Manifest.Network != "net/alt.json" {
		t.Errorf("materialized manifest network = %q, want the EFFECTIVE net/alt.json", v.Manifest.Network)
	}
	base, err := Load(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	if v.Hash() == base.Hash() {
		t.Error("network replacement did not move the hash")
	}

	// The variant hashes identically to the equivalent hand-written
	// scenario (M12 addendum §5) — the regression test for the
	// eff.Network divergence the external review caught.
	hand := t.TempDir()
	writeNetAlt(t, hand, "net/alt.json")
	writeFile(t, hand, ManifestFile, strings.NewReplacer(
		"id: test-baseline", "id: new-net",
		"network: network.json", "network: net/alt.json",
	).Replace(goodManifest))
	writeFile(t, hand, "demand/main.yaml", strings.Replace(variantDemand, "origin: a_0", "origin: c_0", 1))
	h, err := Load(hand)
	if err != nil {
		t.Fatal(err)
	}
	if v.Hash() != h.Hash() {
		t.Errorf("network-replacing variant hash %s != hand-written equivalent %s", v.Hash(), h.Hash())
	}
}

// An anchor naming a flow that exists but carries no id is a hard error —
// the M12 addendum's distinct mode: patches anchor by durable id only.
func TestVariantAnchorRequiresID(t *testing.T) {
	baseDir, variantDir := variantPair(t, `format_version: 1
id: x
base: ../../base
patches:
  - {part: demand/main.yaml, flow: wb-commute, set: {veh_per_h: 100}}
`)
	writeFile(t, baseDir, "demand/main.yaml", goodDemand) // the flow has no id
	_, err := Load(variantDir)
	if err == nil || !strings.Contains(err.Error(), "no flow with id") {
		t.Fatalf("err = %v, want id-less anchor rejection", err)
	}
}

// A variant carrying its OWN network.json over the base's network.json is
// the conventional layout: replacement shadows the base network's hash
// frame, it does not collide with it (round-2 external review).
func TestVariantNetworkShadowing(t *testing.T) {
	baseDir, variantDir := variantPair(t, `format_version: 1
id: shadow-net
base: ../../base
network: network.json
patches:
  - part: demand/main.yaml
    flow: wb-commute
    set:
      origin: c_0
`)
	writeNetAlt(t, variantDir, "network.json")
	v, err := Load(variantDir)
	if err != nil {
		t.Fatalf("Load with shadowing network: %v", err)
	}
	if v.Manifest.Network != "network.json" {
		t.Errorf("manifest network = %q, want network.json", v.Manifest.Network)
	}
	// Hash-equals-hand-written on the shadowing layout too.
	hand := t.TempDir()
	writeNetAlt(t, hand, "network.json")
	writeFile(t, hand, ManifestFile, strings.Replace(goodManifest, "id: test-baseline", "id: shadow-net", 1))
	writeFile(t, hand, "demand/main.yaml", strings.Replace(variantDemand, "origin: a_0", "origin: c_0", 1))
	h, err := Load(hand)
	if err != nil {
		t.Fatal(err)
	}
	if v.Hash() != h.Hash() {
		t.Errorf("shadowing variant hash %s != hand-written equivalent %s", v.Hash(), h.Hash())
	}
	_ = baseDir
}

// Type-list override replaces wholesale: dropping truck from the override
// invalidates the base flow's vtype mix at load.
func TestVariantTypeOverride(t *testing.T) {
	_, variantDir := variantPair(t, `format_version: 1
id: cars-only
base: ../../base
types: [car]
`)
	_, err := Load(variantDir)
	if err == nil || !strings.Contains(err.Error(), "not in the scenario type list") {
		t.Fatalf("err = %v, want vtype rejection against the effective type list", err)
	}
}

func TestVariantErrors(t *testing.T) {
	cases := []struct {
		name    string
		variant string
		want    string
	}{
		{"missing anchor", `format_version: 1
id: x
base: ../../base
patches:
  - {part: demand/main.yaml, flow: nope, set: {veh_per_h: 100}}
`, "no flow with id"},
		{"null is deletion", `format_version: 1
id: x
base: ../../base
patches:
  - part: demand/main.yaml
    flow: wb-commute
    set:
      vtypes:
        truck: null
`, "addition only"},
		{"empty set", `format_version: 1
id: x
base: ../../base
patches:
  - {part: demand/main.yaml, flow: wb-commute, set: {}}
`, "non-empty mapping"},
		{"unknown key in set", `format_version: 1
id: x
base: ../../base
patches:
  - {part: demand/main.yaml, flow: wb-commute, set: {veh_per_hour: 100}}
`, "field veh_per_hour not found"},
		{"coercion in set", `format_version: 1
id: x
base: ../../base
patches:
  - {part: demand/main.yaml, flow: wb-commute, set: {veh_per_h: lots}}
`, "wants !!float"},
		{"patch not a base demand part", `format_version: 1
id: x
base: ../../base
patches:
  - {part: demand/other.yaml, flow: wb-commute, set: {veh_per_h: 100}}
`, "not a demand part of the base"},
		{"absolute base", `format_version: 1
id: x
base: /tmp/base
`, "clean relative"},
		{"backslash base", "format_version: 1\nid: x\nbase: '..\\base'\n", "forward slashes"},
		{"unclean base", `format_version: 1
id: x
base: ../../variants/../base
`, "clean relative"},
		{"missing base", `format_version: 1
id: x
`, "missing base"},
		{"duplicate anchor", `format_version: 1
id: x
base: ../../base
patches:
  - {part: demand/main.yaml, flow: wb-commute, set: {veh_per_h: 100}}
  - {part: demand/main.yaml, flow: wb-commute, set: {veh_per_h: 200}}
`, "duplicate anchor"},
		{"patching id is forbidden", `format_version: 1
id: x
base: ../../base
patches:
  - {part: demand/main.yaml, flow: wb-commute, set: {id: renamed, veh_per_h: 100}}
`, "anchor is immutable"},
		{"control ref names the variant manifest", `format_version: 1
id: x
base: ../../base
control: [variant.yaml]
`, "names a manifest file"},
		{"null override is a removal directive", `format_version: 1
id: x
base: ../../base
seed: null
`, "addition only"},
		{"empty types override", `format_version: 1
id: x
base: ../../base
types: []
`, "empty override"},
		{"empty params override", `format_version: 1
id: x
base: ../../base
params: {}
`, "empty override"},
		{"empty spawner override", `format_version: 1
id: x
base: ../../base
spawner: {}
`, "empty override"},
		{"duplicate key in set", `format_version: 1
id: x
base: ../../base
patches:
  - part: demand/main.yaml
    flow: wb-commute
    set: {veh_per_h: 100, veh_per_h: 200}
`, "duplicate mapping key"},
		{"drive-qualified base", `format_version: 1
id: x
base: C:/base
`, "drive-qualified"},
		{"empty network override", `format_version: 1
id: x
base: ../../base
network: ""
`, "empty override"},
		{"network shadows base part", `format_version: 1
id: x
base: ../../base
network: demand/main.yaml
`, "duplicate part reference"},
		{"newer version", strings.Replace(identityVariant, "format_version: 1", "format_version: 2", 1), "unsupported format_version 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, variantDir := variantPair(t, tc.variant)
			_, err := Load(variantDir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

// Added part paths may not shadow a base part path (the merged directory
// is the hash's frame of reference) — including the base's network path.
func TestVariantPartCollision(t *testing.T) {
	_, variantDir := variantPair(t, `format_version: 1
id: x
base: ../../base
demand:
  - demand/main.yaml
`)
	writeFile(t, variantDir, "demand/main.yaml", variantDemand)
	_, err := Load(variantDir)
	if err == nil || !strings.Contains(err.Error(), "collides with a base part") {
		t.Fatalf("err = %v, want collision rejection", err)
	}

	_, variantDir2 := variantPair(t, `format_version: 1
id: x
base: ../../base
demand:
  - network.json
`)
	writeFile(t, variantDir2, "network.json", variantDemand)
	_, err = Load(variantDir2)
	if err == nil || !strings.Contains(err.Error(), "collides with a base part") {
		t.Fatalf("base-network shadow: err = %v, want collision rejection", err)
	}
}

// Single-level composition: a variant's base must be a plain scenario.
func TestVariantChainingRejected(t *testing.T) {
	root := t.TempDir()
	mid := filepath.Join(root, "mid")
	if err := os.MkdirAll(mid, 0o755); err != nil {
		t.Fatal(err)
	}
	writeNet(t, mid)
	writeFile(t, mid, ManifestFile, goodManifest)
	writeFile(t, mid, "demand/main.yaml", variantDemand)
	// mid is itself turned into a variant directory.
	if err := os.Remove(filepath.Join(mid, ManifestFile)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, mid, VariantManifest, identityVariant)
	leaf := filepath.Join(root, "leaf")
	writeFile(t, leaf, VariantManifest, `format_version: 1
id: leaf
base: ../mid
`)
	_, err := Load(leaf)
	if err == nil || !strings.Contains(err.Error(), "single-level") {
		t.Fatalf("err = %v, want chaining rejection", err)
	}
}

// A directory holding both manifests is neither scenario nor variant.
func TestBothManifestsRejected(t *testing.T) {
	dir := goodScenario(t)
	writeFile(t, dir, VariantManifest, identityVariant)
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("err = %v, want both-manifests rejection", err)
	}
}

// fmt touches only the variant's own files — the base is never rewritten.
func TestVariantFormatLeavesBaseAlone(t *testing.T) {
	baseDir, variantDir := variantPair(t, `# peak variant
id: test-peak-x2
base: ../../base
format_version: 1
patches:
  - {flow: wb-commute, part: demand/main.yaml, set: {veh_per_h: 1800}}
`)
	baseBefore, err := os.ReadFile(filepath.Join(baseDir, "demand/main.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := Load(variantDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Format(variantDir); err != nil {
		t.Fatalf("Format: %v", err)
	}
	baseAfter, err := os.ReadFile(filepath.Join(baseDir, "demand/main.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(baseBefore) != string(baseAfter) {
		t.Error("fmt rewrote a BASE file")
	}
	data, _ := os.ReadFile(filepath.Join(variantDir, VariantManifest))
	if !strings.Contains(string(data), "# peak variant") {
		t.Errorf("fmt lost the variant comment:\n%s", data)
	}
	// Keys sorted canonically (base before format_version before id).
	if strings.Index(string(data), "base:") > strings.Index(string(data), "id:") {
		t.Errorf("variant.yaml not canonical:\n%s", data)
	}
	after, err := Load(variantDir)
	if err != nil {
		t.Fatal(err)
	}
	if before.Hash() != after.Hash() {
		t.Error("hash moved across variant fmt")
	}
	if err := Format(variantDir); err != nil {
		t.Fatalf("Format again: %v", err)
	}
}
