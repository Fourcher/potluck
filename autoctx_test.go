package main

import "testing"

func TestAutoCtx(t *testing.T) {
	h := &Host{cfg: loadConfig(t.TempDir()), id: &Identity{FP: "main"}, nodes: map[string]*Node{
		"pc": {Online: true, Info: NodeInfo{Name: "pc", Cores: 8, Threads: 16, RAMTotal: 64 << 30, RAMAvail: 60 << 30, Limits: Limits{Pedal: 100, Enabled: true}}},
	}}
	// 24 layers of 200 MB with a cache of 8 heads x 128 per layer: 96 KB a token at 16 bits.
	m := fakeModel(24, 200<<20, 500<<20)
	m.KVHeads, m.KeyLen, m.ValLen, m.CtxTrain = []int{8}, 128, 128, 1<<20
	if c := h.autoCtx(m, "f.gguf", "", "pc"); c < 32768 {
		t.Fatalf("plenty of memory: got %d, want at least 32768", c)
	}
	m.CtxTrain = 16384
	if c := h.autoCtx(m, "f.gguf", "", "pc"); c != 16384 {
		t.Fatalf("never more than the model was trained for: got %d", c)
	}
	// A computer with room for the model but not a 32k cache gets less, not the disk.
	m.CtxTrain = 1 << 20
	h.nodes["pc"].Info.RAMTotal, h.nodes["pc"].Info.RAMAvail = 9<<30, 8<<30
	if c := h.autoCtx(m, "f.gguf", "", "pc"); c >= 32768 || c < 4096 {
		t.Fatalf("tight memory: got %d", c)
	}
}
