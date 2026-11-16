package main

import (
	"math/rand"
	"testing"
)

func fakeModel(layers int, layerBytes, other int64) *ModelInfo {
	m := &ModelInfo{Layers: layers, OtherBytes: other, LayerBytes: make([]int64, layers)}
	for i := range m.LayerBytes {
		m.LayerBytes[i] = layerBytes
	}
	return m
}

// checkPlan verifies that llama.cpp's own layer rule, fed the plan's -ngl and --tensor-split,
// puts exactly the planned number of layers on every device.
func checkPlan(t *testing.T, m *ModelInfo, p *Plan) {
	t.Helper()
	if !p.Fits {
		return
	}
	split := parseSplit(p.TensorSplit)
	counts := map[int]int{}
	for il := 0; il < m.Layers; il++ {
		counts[deviceOfLayer(il, m.Layers, p.NGL, split)]++
	}
	head := p.Shares[0]
	if counts[-1] != head.CPULayers {
		t.Fatalf("head CPU: planned %d layers, llama.cpp rule gives %d (ngl %d, split %s)", head.CPULayers, counts[-1], p.NGL, p.TensorSplit)
	}
	if len(split) != len(p.DevLayers) && p.NGL > 0 {
		t.Fatalf("split %s has %d entries for %d devices", p.TensorSplit, len(split), len(p.DevLayers))
	}
	total := head.CPULayers
	perOwner := map[string]int{}
	for dev, want := range p.DevLayers {
		if counts[dev] != want {
			t.Fatalf("device %d (%s): planned %d layers, rule gives %d (ngl %d, split %s)", dev, p.DevOwner[dev], want, counts[dev], p.NGL, p.TensorSplit)
		}
		total += want
		perOwner[p.DevOwner[dev]] += want
	}
	for _, s := range p.Shares[1:] {
		if perOwner[s.ID] != s.CPULayers+s.GPULayers {
			t.Fatalf("helper %s: share says %d layers, its devices hold %d", s.Name, s.CPULayers+s.GPULayers, perOwner[s.ID])
		}
	}
	if perOwner[head.ID] != head.GPULayers {
		t.Fatalf("head GPU: share says %d layers, its device holds %d", head.GPULayers, perOwner[head.ID])
	}
	if total != m.Layers {
		t.Fatalf("plan covers %d of %d layers", total, m.Layers)
	}
	// The output layer must land on a device that holds something (or the CPU when nothing is offloaded).
	if p.NGL > 0 {
		out := deviceOfLayer(m.Layers, m.Layers, p.NGL, split)
		if out < 0 || counts[out] == 0 {
			t.Fatalf("output layer on device %d which holds no layers (split %s)", out, p.TensorSplit)
		}
	}
}

// The real cluster this was first run on: a 36.6 GB, 48-layer model over a 30 GB PC with a
// 4 GB GPU and two 16 GB laptops.
func TestPlanRealCluster(t *testing.T) {
	const mib = int64(1) << 20
	m := fakeModel(48, 745_000_000, 860_000_000)
	// What each machine offered on the day, after the planner's own safety reserve.
	nodes := []PlanNode{
		{ID: "monolith", Name: "Monolith", CPUBytes: 17_300 * mib, GPUBytes: 2859 * mib},
		{ID: "llmos", Name: "LLMOS", CPUBytes: 9_180 * mib},
		{ID: "macbook", Name: "MacBook", CPUBytes: 9_900 * mib},
	}
	p := planLayout(m, nodes, "")
	if !p.Fits || p.Head != "monolith" || !p.NeedsPool || p.Machines != 3 {
		t.Fatalf("unexpected plan: %+v", p)
	}
	checkPlan(t, m, p)
	// The hand-tuned layout that ran was 22 CPU + 2 GPU on the head and 12 + 12 on the laptops.
	if g := p.Shares[0].GPULayers; g != 2 {
		t.Fatalf("expected 2 layers on the 4 GB GPU, got %d", g)
	}
	for _, s := range p.Shares[1:] {
		if s.CPULayers < 10 || s.CPULayers > 13 {
			t.Fatalf("laptop %s got %d layers, expected about 12", s.Name, s.CPULayers)
		}
	}
	t.Logf("ngl %d split %s shares %+v", p.NGL, p.TensorSplit, p.Shares)
}

func TestPlanTooBig(t *testing.T) {
	m := fakeModel(48, 2<<30, 1<<30)
	p := planLayout(m, []PlanNode{{ID: "a", CPUBytes: 8 << 30}, {ID: "b", CPUBytes: 8 << 30}}, "")
	if p.Fits || p.ShortBy <= 0 {
		t.Fatalf("expected a model that does not fit, got %+v", p)
	}
}

func TestPlanRandom(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 20000; i++ {
		layers := 4 + r.Intn(90)
		m := fakeModel(layers, int64(50+r.Intn(900))<<20, int64(r.Intn(900))<<20)
		var nodes []PlanNode
		for n := 0; n < 1+r.Intn(5); n++ {
			pn := PlanNode{ID: string(rune('a' + n)), Name: string(rune('A' + n)), CPUBytes: int64(r.Intn(40)) << 30}
			switch r.Intn(5) {
			case 0:
				pn.GPUBytes, pn.HasGPU = int64(1+r.Intn(24))<<30, true
			case 4:
				pn.GPUBytes, pn.HasGPU, pn.TwoDev = int64(r.Intn(24))<<30, true, true // a helper with a graphics card, sometimes a full one
			case 1:
				pn.Unified, pn.GPUBytes, pn.CPUBytes = true, int64(4+r.Intn(60))<<30, 0
			}
			nodes = append(nodes, pn)
		}
		p := planLayout(m, nodes, "")
		checkPlan(t, m, p)
	}
}

func TestLimitsSchedule(t *testing.T) {
	l := Limits{Pedal: 100, Enabled: true, SchedOn: true, SchedStart: "22:00", SchedEnd: "07:00"}
	at := func(h, m int) bool { return l.ActiveNow(timeAt(h, m)) }
	if !at(23, 0) || !at(3, 30) || at(12, 0) || at(7, 0) || !at(22, 0) {
		t.Fatal("overnight window is wrong")
	}
	l.SchedStart, l.SchedEnd = "09:00", "17:00"
	if !at(12, 0) || at(18, 0) || at(8, 59) {
		t.Fatal("daytime window is wrong")
	}
	l.Enabled = false
	if at(12, 0) {
		t.Fatal("a switched-off computer must never be active")
	}
}

func TestCompactToken(t *testing.T) {
	fp := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	tok := compactToken("192.168.1.20", fp, "0123456789abcdef")
	if len(tok) != 48 {
		t.Fatalf("token length %d", len(tok))
	}
	j, err := parseCompactToken(tok)
	if err != nil || j.Addrs[0] != "192.168.1.20" || j.Port != portLink || j.FP != fp[:32] || j.Secret != "0123456789abcdef" {
		t.Fatalf("round trip failed: %+v %v", j, err)
	}
	if m := tokenInName.FindStringSubmatch("potluck-join-" + tok + " (1).exe"); m == nil || m[1] != tok {
		t.Fatal("token not found in a renamed download")
	}
}

// A helper with a graphics card offers the card first, then its memory.
func TestPlanHelperGPU(t *testing.T) {
	const gb = int64(1) << 30
	m := fakeModel(48, 380<<20, 400<<20) // about 18 GB
	nodes := []PlanNode{
		{ID: "laptop", Name: "Laptop", CPUBytes: 10 * gb},
		{ID: "pc", Name: "PC", CPUBytes: 6 * gb, GPUBytes: 3 * gb, HasGPU: true, TwoDev: true},
		{ID: "old", Name: "Old", CPUBytes: 5 * gb},
	}
	p := planLayout(m, nodes, "laptop")
	checkPlan(t, m, p)
	var pc *PlanShare
	for i := range p.Shares {
		if p.Shares[i].ID == "pc" {
			pc = &p.Shares[i]
		}
	}
	if !p.Fits || pc == nil || pc.GPULayers == 0 || pc.CPULayers == 0 {
		t.Fatalf("expected the PC to use its graphics card and its memory: %+v", p.Shares)
	}
}

func TestPlanSolo(t *testing.T) {
	const gb = int64(1) << 30
	m := fakeModel(48, 745_000_000, 860_000_000)
	p := planSolo(m, PlanNode{ID: "pc", Name: "PC", CPUBytes: 19 * gb, GPUBytes: 2859 << 20, HasGPU: true, RAMTotal: 30 * gb})
	if !p.Fits || !p.Solo || p.Overflow <= 0 || p.Shares[0].GPULayers != 2 || p.Shares[0].CPULayers != 46 || p.NGL != 3 {
		t.Fatalf("unexpected solo plan: %+v", p)
	}
	if q := planSolo(m, PlanNode{ID: "small", Name: "Small", CPUBytes: 9 * gb, RAMTotal: 16 * gb}); q.Fits {
		t.Fatal("a 36 GB model on a 16 GB computer should be refused")
	}
}
