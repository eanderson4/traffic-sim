package scenario

// variant.go — ADR-0012 §4 overlays (M12 addendum "M12 overlay design").
// A variant directory holds variant.yaml INSTEAD of scenario.yaml, naming
// its base scenario plus manifest overrides, added part files, and patches
// anchored to base demand flows by (part path, flow id). Loading
// materializes the overlay into a Scenario indistinguishable from a
// hand-authored one — same hash protocol, same RunSpec, same
// (content-hash, seed) run key. The base directory is never modified.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"traffic-sim/engine"
)

// VariantManifest is the manifest name of a variant directory. A directory
// holds scenario.yaml OR variant.yaml, never both — one manifest per
// directory, so a directory's kind is unambiguous and composition cycles
// are structurally impossible.
const VariantManifest = "variant.yaml"

// Variant is variant.yaml (strict YAML, format_version 1): identity, the
// base reference, manifest overrides (present fields REPLACE the base's
// wholesale — named-field replacement, not a removal directive), added
// part files, and patches. Single-level composition only: the base must
// not itself be a variant.
type Variant struct {
	FormatVersion int      `yaml:"format_version"`
	ID            string   `yaml:"id"`
	Base          string   `yaml:"base"`
	Seed          *uint64  `yaml:"seed,omitempty"`
	Ticks         *uint64  `yaml:"ticks,omitempty"`
	Params        *Params  `yaml:"params,omitempty"`
	Types         []string `yaml:"types,omitempty"`
	Spawner       *Spawner `yaml:"spawner,omitempty"`
	Network       string   `yaml:"network,omitempty"`
	Demand        []string `yaml:"demand,omitempty"`
	Control       []string `yaml:"control,omitempty"`
	Metrics       []string `yaml:"metrics,omitempty"`
	Patches       []Patch  `yaml:"patches,omitempty"`
}

// Patch modifies one base demand flow, anchored by (part path, flow id) —
// the part path disambiguates flow ids across files. Set merges into the
// flow at the YAML node level: mappings key-wise recursively, sequences
// and scalars wholesale (a slices rate program patches as one entity).
// Nulls are forbidden (no deletion — addition-only composition), and
// patches MODIFY only: new flows arrive via added part files.
type Patch struct {
	Part string    `yaml:"part"`
	Flow string    `yaml:"flow"`
	Set  yaml.Node `yaml:"set"`
}

// loadVariant materializes a variant directory (see the package doc and
// the ADR-0012 M12 addendum for the semantics this implements).
func loadVariant(dir string) (*Scenario, error) {
	vdata, err := os.ReadFile(filepath.Join(dir, VariantManifest))
	if err != nil {
		return nil, fmt.Errorf("scenario: %w", err)
	}
	var v Variant
	if err := strictDecode(vdata, &v); err != nil {
		return nil, fmt.Errorf("variant %s: %w", VariantManifest, err)
	}
	// Nulls are forbidden everywhere in variant.yaml, not just in patch
	// sets: pointer/slice override fields decode a null to nil, which
	// reads as ABSENT — a silent removal directive (ADR-0012 §4 forbids
	// them; present fields replace, nothing removes).
	var vdoc yaml.Node
	if err := yaml.Unmarshal(vdata, &vdoc); err != nil {
		return nil, fmt.Errorf("variant %s: %w", VariantManifest, err)
	}
	if err := rejectNull(&vdoc); err != nil {
		return nil, fmt.Errorf("variant %s: %w", VariantManifest, err)
	}
	// An explicit empty-string network decodes identically to an omitted
	// one — catch it at the node level, same discipline as the empty
	// types/params/spawner overrides.
	if n := mappingValue(docRoot(&vdoc), "network"); n != nil && n.Kind == yaml.ScalarNode && n.Value == "" {
		return nil, fmt.Errorf("variant %s: network: %q is an empty override — omit the field to inherit the base's network", VariantManifest, "")
	}
	if err := validateVariant(&v); err != nil {
		return nil, fmt.Errorf("variant %s: %w", VariantManifest, err)
	}
	// Single-level composition: the base must be a plain scenario.
	baseDir := filepath.Join(dir, filepath.FromSlash(v.Base))
	if _, err := os.Stat(filepath.Join(baseDir, VariantManifest)); err == nil {
		return nil, fmt.Errorf("variant %s: base %q is itself a variant — chaining is deferred (single-level composition, ADR-0012 M12 addendum)", VariantManifest, v.Base)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("variant base %q: %w", v.Base, err)
	}
	base, err := Load(baseDir)
	if err != nil {
		return nil, fmt.Errorf("variant base %q: %w", v.Base, err)
	}

	// Effective manifest: base fields, variant overrides, merged part
	// lists. An added part path colliding with a base part path is a hard
	// error — the merged directory is the hash's frame of reference.
	eff := base.Manifest
	eff.ID = v.ID
	if v.Seed != nil {
		eff.Seed = *v.Seed
	}
	if v.Ticks != nil {
		eff.Ticks = *v.Ticks
	}
	if v.Params != nil {
		eff.Params = *v.Params
	}
	if v.Types != nil {
		eff.Types = v.Types
	}
	if v.Spawner != nil {
		eff.Spawner = v.Spawner
	}
	if v.Network != "" {
		// Set BEFORE validateManifest so its checks (duplicate frames,
		// manifest-name refs) see the effective network, and the
		// materialized manifest names it — the variant hashes
		// identically to its hand-written equivalent (M12 addendum §5).
		eff.Network = v.Network
	}
	baseRefs := make(map[string]bool)
	baseRefs[base.Manifest.Network] = true
	for _, r := range append(append(append([]string{}, base.Manifest.Demand...), base.Manifest.Control...), base.Manifest.Metrics...) {
		baseRefs[r] = true
	}
	seen := make(map[string]bool)
	for _, r := range append(append(append([]string{}, v.Demand...), v.Control...), v.Metrics...) {
		if baseRefs[r] {
			return nil, fmt.Errorf("variant %s: added part %q collides with a base part path", VariantManifest, r)
		}
		if seen[r] {
			return nil, fmt.Errorf("variant %s: duplicate added part %q", VariantManifest, r)
		}
		seen[r] = true
	}
	eff.Demand = append(append([]string{}, base.Manifest.Demand...), v.Demand...)
	eff.Control = append(append([]string{}, base.Manifest.Control...), v.Control...)
	eff.Metrics = append(append([]string{}, base.Manifest.Metrics...), v.Metrics...)
	if err := validateManifest(&eff); err != nil {
		return nil, fmt.Errorf("variant %s (materialized manifest): %w", VariantManifest, err)
	}

	// The base's captured hash snapshot is the ONLY source for inherited
	// part bytes — re-reading the base directory after Load could hash
	// bytes that were never compiled or validated (a concurrent edit
	// would move content under a validated identity).
	baseParts := make(map[string][]byte, len(base.parts))
	for _, p := range base.parts {
		baseParts[p.rel] = p.data
	}

	// Effective network: the variant's whole-network replacement (§4's
	// degenerate case) or the base's. ALL demand validation below runs
	// against the EFFECTIVE network's origins and the effective type
	// list — a replacement that invalidates base demand fails HERE, at
	// variant load, not at spawn time.
	netPath, netRel := base.NetPath, base.Manifest.Network
	origins := base.origins
	rawNet := baseParts[base.Manifest.Network] // already canonical JSON
	if v.Network != "" {
		// The replacement joins the conceptual merged directory. Shadowing
		// the BASE network's own rel path is the conventional layout (a
		// variant carrying its own network.json) — only the effective
		// network occupies that frame. Any OTHER part path (base or
		// added) is a hard error: one hash frame per rel path.
		if v.Network != base.Manifest.Network && (baseRefs[v.Network] || seen[v.Network]) {
			return nil, fmt.Errorf("variant %s: network %q collides with a part path in the merged directory", VariantManifest, v.Network)
		}
		p, err := resolvePart(dir, v.Network)
		if err != nil {
			return nil, fmt.Errorf("variant %s: network: %w", VariantManifest, err)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("variant network: %w", err)
		}
		var nf engine.NetFile
		if err := json.Unmarshal(raw, &nf); err != nil {
			return nil, fmt.Errorf("variant network %s: %w", v.Network, err)
		}
		net, err := engine.CompileNet(&nf)
		if err != nil {
			return nil, fmt.Errorf("variant network %s: %w", v.Network, err)
		}
		origins = make(map[string]bool, len(net.Origins))
		for _, l := range net.Origins {
			origins[l.ID] = true
		}
		netPath, netRel, rawNet = p, v.Network, canonicalJSON(raw, v.Network)
	}
	typeSet := make(map[string]bool, len(eff.Types))
	for _, t := range eff.Types {
		typeSet[t] = true
	}

	s := &Scenario{Dir: dir, Manifest: eff, NetPath: netPath, origins: origins}
	hm := eff
	hm.Seed, hm.Ticks = 0, 0 // run coordinates stripped (see Load)
	s.parts = append(s.parts, hashPart{rel: ManifestFile, data: canonicalYAML(&hm)})
	s.parts = append(s.parts, hashPart{rel: netRel, data: rawNet})

	patchesByPart := make(map[string][]Patch)
	for _, p := range v.Patches {
		patchesByPart[p.Part] = append(patchesByPart[p.Part], p)
	}
	for i, ref := range base.Manifest.Demand {
		raw := baseParts[ref] // canonical bytes captured by the base Load
		if ps, ok := patchesByPart[ref]; ok {
			patched, err := patchDemandPart(raw, ref, ps)
			if err != nil {
				return nil, err
			}
			delete(patchesByPart, ref)
			df, canonical, err := parseDemand(patched, origins, typeSet)
			if err != nil {
				return nil, fmt.Errorf("variant demand %s (after patching — line numbers cite the patched document, not the base file): %w", ref, err)
			}
			s.Demands = append(s.Demands, df)
			s.parts = append(s.parts, hashPart{rel: ref, data: canonical})
			continue
		}
		// Unpatched: reuse the base's loaded model and captured canonical
		// bytes, re-validated against the EFFECTIVE origins and types.
		df := base.Demands[i]
		if err := validateDemand(df, origins, typeSet); err != nil {
			return nil, fmt.Errorf("variant demand %s: %w", ref, err)
		}
		s.Demands = append(s.Demands, df)
		s.parts = append(s.parts, hashPart{rel: ref, data: raw})
	}
	if len(patchesByPart) > 0 {
		leftover := make([]string, 0, len(patchesByPart))
		for ref := range patchesByPart {
			leftover = append(leftover, ref)
		}
		sort.Strings(leftover)
		return nil, fmt.Errorf("variant %s: patch targets [%s]: not a demand part of the base (patches modify base demand parts only)", VariantManifest, strings.Join(leftover, ", "))
	}
	for _, ref := range v.Demand {
		pth, err := resolvePart(dir, ref)
		if err != nil {
			return nil, fmt.Errorf("variant demand: %w", err)
		}
		df, canonical, err := loadDemandFile(pth, origins, typeSet)
		if err != nil {
			return nil, fmt.Errorf("variant demand %s: %w", ref, err)
		}
		s.Demands = append(s.Demands, df)
		s.parts = append(s.parts, hashPart{rel: ref, data: canonical})
	}
	// Control/metrics stay raw pass-through (their grammars land with the
	// observability ADR): base parts come from the captured snapshot,
	// added parts from the variant; patches never target them.
	for _, ref := range append(append([]string{}, base.Manifest.Control...), base.Manifest.Metrics...) {
		s.parts = append(s.parts, hashPart{rel: ref, data: baseParts[ref]})
	}
	for _, ref := range append(append([]string{}, v.Control...), v.Metrics...) {
		pth, err := resolvePart(dir, ref)
		if err != nil {
			return nil, fmt.Errorf("variant part: %w", err)
		}
		raw, err := os.ReadFile(pth)
		if err != nil {
			return nil, fmt.Errorf("variant part %s: %w", ref, err)
		}
		s.parts = append(s.parts, hashPart{rel: ref, data: raw})
	}
	if (eff.Spawner == nil || eff.Spawner.RatePerLaneHour == 0) && len(eff.Demand) == 0 {
		return nil, fmt.Errorf("variant %s (materialized): no demand — the overlay disabled the base's spawner without adding demand", VariantManifest)
	}
	return s, nil
}

func validateVariant(v *Variant) error {
	if v.FormatVersion != FormatVersion {
		return fmt.Errorf("unsupported format_version %d (loader supports %d)", v.FormatVersion, FormatVersion)
	}
	if v.ID == "" {
		return errors.New("missing id")
	}
	if v.Base == "" {
		return errors.New("missing base")
	}
	if strings.ContainsRune(v.Base, '\\') {
		return fmt.Errorf("base %q: use forward slashes (portable refs)", v.Base)
	}
	if len(v.Base) > 1 && v.Base[1] == ':' {
		return fmt.Errorf("base %q: drive-qualified paths are not portable refs", v.Base)
	}
	if path.IsAbs(v.Base) || path.Clean(v.Base) != v.Base {
		return fmt.Errorf("base %q: must be a clean relative path to the base scenario directory (../ allowed)", v.Base)
	}
	if v.Types != nil && len(v.Types) == 0 {
		return errors.New("types: [] is an empty override — omit the field to inherit the base's list (fail loud, no implicit re-default)")
	}
	if v.Params != nil && *v.Params == (Params{}) {
		return errors.New("params: {} is an empty override — omit the field to inherit (fail loud, no silent reset to engine defaults)")
	}
	if v.Spawner != nil && *v.Spawner == (Spawner{}) {
		return errors.New("spawner: {} is an empty override — omit the field to inherit (fail loud, no silent disable)")
	}
	anchors := make(map[string]bool, len(v.Patches))
	for i, p := range v.Patches {
		if p.Part == "" || p.Flow == "" {
			return fmt.Errorf("patch %d: part and flow are required", i)
		}
		anchor := p.Part + "\x00" + p.Flow
		if anchors[anchor] {
			return fmt.Errorf("patch %d: duplicate anchor (part %q, flow %q) — one patch per flow; later entries would silently overwrite earlier ones", i, p.Part, p.Flow)
		}
		anchors[anchor] = true
		if strings.ContainsRune(p.Part, '\\') || path.IsAbs(p.Part) || path.Clean(p.Part) != p.Part ||
			p.Part == ".." || strings.HasPrefix(p.Part, "../") ||
			(len(p.Part) > 1 && p.Part[1] == ':') {
			return fmt.Errorf("patch %d: part %q must be a clean relative path of a base demand part", i, p.Part)
		}
		if p.Set.Kind != yaml.MappingNode || len(p.Set.Content) == 0 {
			return fmt.Errorf("patch %d (flow %q): set must be a non-empty mapping", i, p.Flow)
		}
		if err := rejectDupKeys(&p.Set); err != nil {
			return fmt.Errorf("patch %d (flow %q): %w", i, p.Flow, err)
		}
		if mappingValue(&p.Set, "id") != nil {
			return fmt.Errorf("patch %d (flow %q): set may not patch id — the anchor is immutable (a rename could silently re-target a later patch)", i, p.Flow)
		}
		if err := rejectNull(&p.Set); err != nil {
			return fmt.Errorf("patch %d (flow %q): %w", i, p.Flow, err)
		}
	}
	return nil
}

// rejectDupKeys closes the one gap yaml.v3 leaves in the strict subset:
// duplicate mapping keys are a decode error only for map/struct
// destinations — a yaml.Node destination (a patch's set) preserves both
// pairs silently, and mergeMapping would apply them last-wins.
func rejectDupKeys(n *yaml.Node) error {
	if n.Kind == yaml.MappingNode {
		seen := make(map[string]bool, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if seen[k] {
				return fmt.Errorf("line %d: duplicate mapping key %q (strict YAML admits no duplicates)", n.Content[i].Line, k)
			}
			seen[k] = true
		}
	}
	for _, c := range n.Content {
		if err := rejectDupKeys(c); err != nil {
			return err
		}
	}
	return nil
}

// rejectNull fences deletion out of patches: null is JSON-Merge-Patch's
// remove, and overlays compose by addition only (ADR-0012 §4 permanent
// refusal — split files instead).
func rejectNull(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return fmt.Errorf("line %d: null values are deletion, and overlays compose by addition only (ADR-0012 §4)", n.Line)
	}
	for _, c := range n.Content {
		if err := rejectNull(c); err != nil {
			return err
		}
	}
	return nil
}

// patchDemandPart applies patches to a base demand part at the YAML node
// level and re-encodes. The result is strict-decoded and semantically
// validated by the caller exactly like a hand-authored file — unknown
// keys, scalar coercions, and bad references all fail loud at apply time.
func patchDemandPart(data []byte, rel string, patches []Patch) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("patch %s: %w", rel, err)
	}
	flows := mappingValue(docRoot(&root), "flows")
	if flows == nil || flows.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("patch %s: no flows sequence", rel)
	}
	for _, p := range patches {
		target := findFlow(flows, p.Flow)
		if target == nil {
			return nil, fmt.Errorf("patch %s: no flow with id %q (a typo'd anchor must never silently add — new flows arrive via added part files)", rel, p.Flow)
		}
		mergeMapping(target, &p.Set)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, fmt.Errorf("patch %s: re-encode: %w", rel, err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("patch %s: re-encode: %w", rel, err)
	}
	return buf.Bytes(), nil
}

func docRoot(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return n.Content[0]
	}
	return n
}

// mappingValue returns m's value node for key, or nil (m not a mapping or
// key absent).
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// findFlow locates the flow mapping whose id scalar equals the anchor.
// Duplicate anchors can't occur — the base validated duplicate flow ids
// out at load.
func findFlow(flows *yaml.Node, id string) *yaml.Node {
	for _, f := range flows.Content {
		if idNode := mappingValue(f, "id"); idNode != nil && idNode.Kind == yaml.ScalarNode && idNode.Value == id {
			return f
		}
	}
	return nil
}

// mergeMapping merges src into dst key-wise: where both values are
// mappings it recurses; sequences and scalars replace wholesale (the
// documented M12 patch semantic). Nulls were fenced by validateVariant.
func mergeMapping(dst, src *yaml.Node) {
	for i := 0; i+1 < len(src.Content); i += 2 {
		k, sv := src.Content[i], src.Content[i+1]
		dv := mappingValue(dst, k.Value)
		if dv != nil && dv.Kind == yaml.MappingNode && sv.Kind == yaml.MappingNode {
			mergeMapping(dv, sv)
			continue
		}
		if dv != nil {
			for j := 0; j+1 < len(dst.Content); j += 2 {
				if dst.Content[j].Value == k.Value {
					dst.Content[j+1] = sv
				}
			}
		} else {
			dst.Content = append(dst.Content, k, sv)
		}
	}
}
