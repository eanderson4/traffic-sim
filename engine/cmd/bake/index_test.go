package main

import (
	"testing"
)

// index_test.go — the content key (ADR-0023 §8): stable for identical
// inputs, different when ANY ingredient of the bake identity moves.

func testIdentity() bakeIdentity {
	return bakeIdentity{
		Stream:       "TS_LOG_x",
		Run:          "x",
		ScenarioHash: "abc123",
		Seed:         7,
		Ticks:        9000,
		RecordDigest: [32]byte{1, 2, 3},
		OverlayNames: []string{"water.geojson"},
		OverlayBytes: [][]byte{[]byte("{}")},
		ConfigDigest: configDigest(currentBakeConfig(5)),
	}
}

func TestContentKeyStable(t *testing.T) {
	a, b := testIdentity(), testIdentity()
	if a.hash12() != b.hash12() {
		t.Fatal("identical bake identities key differently")
	}
	if len(a.hash12()) != 12 {
		t.Fatalf("hash12 %q is %d chars", a.hash12(), len(a.hash12()))
	}
}

func TestContentKeySensitive(t *testing.T) {
	base := testIdentity().hash12()
	mutate := func(name string, f func(*bakeIdentity)) {
		t.Helper()
		id := testIdentity()
		f(&id)
		if id.hash12() == base {
			t.Fatalf("%s: content key unchanged", name)
		}
	}
	mutate("stream", func(id *bakeIdentity) { id.Stream = "TS_LOG_y" })
	mutate("run", func(id *bakeIdentity) { id.Run = "y" })
	mutate("scenario hash", func(id *bakeIdentity) { id.ScenarioHash = "def456" })
	mutate("seed", func(id *bakeIdentity) { id.Seed++ })
	mutate("tick horizon", func(id *bakeIdentity) { id.Ticks++ })
	mutate("record digest", func(id *bakeIdentity) { id.RecordDigest[0]++ })
	mutate("overlay bytes", func(id *bakeIdentity) { id.OverlayBytes[0] = []byte(`{"changed":true}`) })
	mutate("overlay set", func(id *bakeIdentity) { id.OverlayNames = nil; id.OverlayBytes = nil })
	mutate("bake config", func(id *bakeIdentity) {
		cfg := currentBakeConfig(5)
		cfg.ChunkFrames++
		id.ConfigDigest = configDigest(cfg)
	})
}

func TestConfigDigestCoversKnobs(t *testing.T) {
	base := configDigest(currentBakeConfig(5))
	for _, tweak := range []func(*bakeConfig){
		func(c *bakeConfig) { c.BakeEveryTicks = 10 },
		func(c *bakeConfig) { c.LaneEveryFrames = 5 },
		func(c *bakeConfig) { c.ChunkFrames = 60 },
		func(c *bakeConfig) { c.QuantXYStepM = 0.2 },
		func(c *bakeConfig) { c.BrotliQuality = 6 },
		func(c *bakeConfig) { c.MinzoomPolicy = "changed" },
		func(c *bakeConfig) { c.TSRBVersion = 2 },
		func(c *bakeConfig) { c.BakeToolVersion = 2 },
	} {
		cfg := currentBakeConfig(5)
		tweak(&cfg)
		if configDigest(cfg) == base {
			t.Fatalf("config digest blind to %+v", cfg)
		}
	}
}
