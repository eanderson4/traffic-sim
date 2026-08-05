package sigctl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"traffic-sim/engine"
	"traffic-sim/engine/natsio"
)

// helpers_test.go — shared fixtures: detector geometry for program "J"
// and a real TSSG frame built through a real engine (the same bytes the
// live plane carries).

func testGeom() *Geom {
	return &Geom{ByProgram: map[string][]*Detector{
		"J": {{Link: 0, X: 0, Y: 0, Dx: 1}, {Link: 1, X: 50, Y: 0, Dx: 1}},
	}}
}

// testSigFrame compiles a tiny signalized junction and encodes its
// program table with the live plane's own encoder.
func testSigFrame(t *testing.T) []byte {
	t.Helper()
	link := 0
	nf := &engine.NetFile{
		Version: 1,
		Name:    "t",
		Lanes: []engine.NetLane{
			{ID: "nA_0", Section: "A", Length: 200, SpeedLimit: 13.89, Origin: true, Successors: []string{"iJ_0"}},
			{ID: "iJ_0", Section: "j:J", Length: 10, SpeedLimit: 13.89, Internal: true, Junction: "J", TL: "J", TLLink: &link, Successors: []string{"nX_0"}},
			{ID: "nX_0", Section: "X", Length: 200, SpeedLimit: 13.89, Exit: true},
		},
		Signals: []engine.NetSignal{{ID: "J", Junction: "J", Phases: []engine.NetSignalPhase{
			{Duration: 10.0, State: "Gr"}, {Duration: 10.0, State: "rG"},
		}}},
	}
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "net.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := engine.NewEngine(engine.RunSpec{
		Net:    engine.NetSpec{Kind: "file", Path: path},
		Params: engine.DefaultParams(),
		Seed:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return natsio.SignalFrame(e)
}
