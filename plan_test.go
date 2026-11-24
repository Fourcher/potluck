package main

import (
	"math/rand"
	"strings"
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

// moeModel is shaped like the 122B mixture-of-experts model measured on the real cluster:
// 48 layers of 747.8 MB, 673 MB of each being experts.
func moeModel() *ModelInfo {
	m := fakeModel(48, 747787264, 858<<20)
	m.OutputBytes = 409 << 20
	m.ExpertBytes = make([]int64, 48)
	for i := range m.ExpertBytes {
		m.ExpertBytes[i] = 642 << 20
	}
	return m
}

func realNodes() []PlanNode {
	return []PlanNode{
		{ID: "mono", Name: "Monolith", CPUBytes: 26405202944, GPUBytes: 3310 << 20, HasGPU: true, RAMTotal: 32 << 30},
		{ID: "mac", Name: "MacBook Pro", CPUBytes: 11424612941, LinkMs: 36},
		{ID: "llmos", Name: "LLMOS", CPUBytes: 9660216832, LinkMs: 16},
	}
}

// The measured fastest layout: the card holds the trunk of the head's layers, and one
// helper, the better connected one, takes what does not fit.
func TestTrunkPlanRealCluster(t *testing.T) {
	m := moeModel()
	p := planLayout(m, realNodes(), "")
	if !p.Fits || !p.CardTrunk {
		t.Fatalf("expected a card-trunk plan, got fits=%v trunk=%v (%s)", p.Fits, p.CardTrunk, p.Reason)
	}
	checkPlan(t, m, p)
	if p.Head != "mono" {
		t.Fatalf("head is %s", p.Head)
	}
	if len(p.WorkerOrder) != 1 {
		t.Fatalf("expected a single helper, got %v", p.WorkerOrder)
	}
	head := p.Shares[0]
	if head.GPULayers != len(p.ExpertCPU) || head.GPULayers < 30 {
		t.Fatalf("card layers %d, expert-in-memory layers %d", head.GPULayers, len(p.ExpertCPU))
	}
	if p.ExpertCPU[0] != m.Layers-head.GPULayers || p.ExpertCPU[len(p.ExpertCPU)-1] != m.Layers-1 {
		t.Fatalf("expert layers %v do not match the card's layers", p.ExpertCPU)
	}
	// Memory check: experts of the card's layers plus the whole front layers fit the offer.
	ram := int64(head.CPULayers)*747787264 + int64(head.GPULayers)*(642<<20)
	if ram > 26405202944 {
		t.Fatalf("head memory %d over its offer", ram)
	}
	card := int64(head.GPULayers)*(747787264-(642<<20)) + m.OutputBytes
	if card > 3310<<20 {
		t.Fatalf("card memory %d over its offer", card)
	}
	t.Logf("head: %d whole layers in memory + %d on the card; helper %s: %d; ngl %d split %s", head.CPULayers, head.GPULayers, p.Shares[1].Name, p.Shares[1].CPULayers, p.NGL, p.TensorSplit)
}

// With no link measurements the plan still uses a single helper when one is enough.
func TestTrunkPlanFewestHelpers(t *testing.T) {
	m := moeModel()
	nodes := realNodes()
	for i := range nodes {
		nodes[i].LinkMs = 0
	}
	p := planLayout(m, nodes, "")
	checkPlan(t, m, p)
	if !p.Fits || len(p.WorkerOrder) != 1 {
		t.Fatalf("expected one helper, got %v", p.WorkerOrder)
	}
}

// A dense model, or a card big enough for whole layers, keeps the whole-layer layout.
func TestTrunkOnlyWhenItHelps(t *testing.T) {
	dense := fakeModel(48, 747787264, 858<<20)
	if p := planLayout(dense, realNodes(), ""); p.CardTrunk {
		t.Fatal("dense model must not use the card-trunk layout")
	} else {
		checkPlan(t, dense, p)
	}
	m := moeModel()
	big := []PlanNode{{ID: "a", Name: "Big", CPUBytes: 64 << 30, GPUBytes: 48 << 30, HasGPU: true}}
	if p := planLayout(m, big, ""); p.CardTrunk || !p.Fits {
		t.Fatal("a card that holds the whole model must take whole layers")
	}
	uni := []PlanNode{{ID: "a", Name: "Mac", GPUBytes: 20 << 30, Unified: true, HasGPU: true}, {ID: "b", Name: "PC", CPUBytes: 30 << 30}}
	if p := planLayout(m, uni, "a"); p.CardTrunk {
		t.Fatal("unified memory must not use the card-trunk layout")
	} else {
		checkPlan(t, m, p)
	}
}

// Random mixture-of-experts clusters: whatever layout comes out must follow llama.cpp's rule
// and stay inside every offer.
func TestTrunkPlanRandom(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 3000; i++ {
		layers := 8 + r.Intn(80)
		lb := int64(100+r.Intn(900)) << 20
		m := fakeModel(layers, lb, int64(100+r.Intn(900))<<20)
		m.OutputBytes = m.OtherBytes / 2
		m.ExpertBytes = make([]int64, layers)
		for l := range m.ExpertBytes {
			m.ExpertBytes[l] = lb * int64(50+r.Intn(45)) / 100
		}
		var nodes []PlanNode
		for n := 0; n < 1+r.Intn(4); n++ {
			pn := PlanNode{ID: string(rune('a' + n)), Name: string(rune('A' + n)), CPUBytes: int64(2+r.Intn(40)) << 30, LinkMs: float64(r.Intn(60))}
			if r.Intn(2) == 0 {
				pn.GPUBytes, pn.HasGPU = int64(1+r.Intn(12))<<30, true
				pn.TwoDev = r.Intn(2) == 0
			}
			nodes = append(nodes, pn)
		}
		p := planLayout(m, nodes, "")
		checkPlan(t, m, p)
		if p.Fits && p.CardTrunk {
			head := p.Shares[0]
			var hn PlanNode
			for _, n := range nodes {
				if n.ID == p.Head {
					hn = n
				}
			}
			var ram, card int64
			for l := 0; l < head.CPULayers; l++ {
				ram += m.LayerBytes[l]
			}
			onCPU := map[int]bool{}
			for _, l := range p.ExpertCPU {
				onCPU[l] = true
			}
			for l := layers - head.GPULayers; l < layers; l++ {
				card += m.trunkBytes(l)
				if onCPU[l] {
					ram += m.expertBytes(l)
				} else {
					card += m.expertBytes(l)
				}
			}
			if ram > hn.CPUBytes || card+m.OutputBytes > hn.GPUBytes {
				t.Fatalf("case %d: head over its offer (ram %d of %d, card %d of %d)", i, ram, hn.CPUBytes, card+m.OutputBytes, hn.GPUBytes)
			}
		}
	}
}

func TestSoloTrunk(t *testing.T) {
	m := moeModel()
	p := planSolo(m, realNodes()[0])
	if !p.Fits || !p.CardTrunk || len(p.ExpertCPU) != p.Shares[0].GPULayers {
		t.Fatalf("solo trunk plan wrong: fits=%v trunk=%v card=%d experts=%d", p.Fits, p.CardTrunk, p.Shares[0].GPULayers, len(p.ExpertCPU))
	}
	if p.Overflow <= 0 {
		t.Fatal("this model is bigger than the head's memory: expected some overflow to disk")
	}
	t.Logf("solo: %d layers' trunk on the card, %d whole in memory, overflow %s", p.Shares[0].GPULayers, p.Shares[0].CPULayers, humanBytes(p.Overflow))
}

func TestExpertTensorNames(t *testing.T) {
	for name, want := range map[string]bool{
		"blk.3.ffn_up_exps.weight": true, "blk.12.ffn_gate_exps.weight": true, "blk.0.ffn_down_chexps.weight": true,
		"blk.3.ffn_up.weight": false, "blk.3.attn_q.weight": false, "blk.3.ffn_gate_inp.weight": false, "output.weight": false,
	} {
		if isExpertTensor(name) != want {
			t.Fatalf("%s: got %v", name, !want)
		}
	}
}

// When one helper is enough at no cost to the head, that is the plan, even if another
// helper has the better connection: one helper measured faster than two.
func TestTrunkPlanPrefersOneHelper(t *testing.T) {
	m := moeModel()
	nodes := realNodes()
	nodes[1].CPUBytes = 15 << 30 // the MacBook with more memory free, as seen on the real cluster
	p := planLayout(m, nodes, "")
	checkPlan(t, m, p)
	if !p.Fits || len(p.WorkerOrder) != 1 || p.Shares[0].CPULayers != 0 {
		t.Fatalf("expected one helper and no whole layers on the head's processor, got %v with %d", p.WorkerOrder, p.Shares[0].CPULayers)
	}
}

// Two helpers are needed either way: the better-connected one is filled first.
func TestTrunkPlanTieGoesToBestLink(t *testing.T) {
	m := moeModel()
	nodes := realNodes()
	nodes[0].CPUBytes = 20 << 30 // a head with less memory: neither helper is enough alone
	p := planLayout(m, nodes, "")
	checkPlan(t, m, p)
	if !p.Fits || len(p.WorkerOrder) != 2 {
		t.Fatalf("expected two helpers, got %v", p.WorkerOrder)
	}
	for _, s := range p.Shares[1:] {
		if s.ID == "llmos" && s.CPULayers != 12 {
			t.Fatalf("the better-connected helper should be filled to its 12 layers, got %d", s.CPULayers)
		}
	}
}

func TestStreamVariant(t *testing.T) {
	for _, c := range []struct {
		info NodeInfo
		want string
	}{
		{NodeInfo{OS: "windows", Arch: "amd64", GPUs: []GPU{{Vendor: "nvidia", ComputeCap: 6.1}}}, "windows-vulkan"},
		{NodeInfo{OS: "windows", Arch: "amd64", GPUs: []GPU{{Vendor: "nvidia", ComputeCap: 8.6}}}, "windows-cuda"},
		{NodeInfo{OS: "windows", Arch: "amd64"}, "windows-vulkan"},
		{NodeInfo{OS: "darwin", Arch: "arm64", OSName: "macOS 27.2.1"}, "macos"},
		{NodeInfo{OS: "darwin", Arch: "arm64", OSName: "macOS 14.6"}, ""},
		{NodeInfo{OS: "darwin", Arch: "amd64", OSName: "macOS 15.7"}, ""},
		{NodeInfo{OS: "linux", Arch: "amd64"}, ""},
		{NodeInfo{OS: "windows", Arch: "arm64"}, ""},
	} {
		if got, why := streamVariant(c.info); got != c.want || (got == "" && why == "") {
			t.Fatalf("%+v: got %q (%q), want %q", c.info, got, why, c.want)
		}
	}
}

func TestPlanStream(t *testing.T) {
	m := moeModel()
	m.Arch, m.FileSize = "qwen35moe", m.TotalBytes()
	pc := PlanNode{ID: "pc", Name: "PC", CPUBytes: 20 << 30, DiskFree: 300 << 30, StreamVariant: "windows-vulkan"}
	if p := planStream(m, pc, false); !p.Fits || !p.Stream || !p.Solo || p.Head != "pc" || p.Overflow <= 0 {
		t.Fatalf("expected a stream plan with overflow, got %+v", p)
	}
	small := pc
	small.DiskFree = 10 << 30
	if p := planStream(m, small, false); p.Fits || p.Reason == "" {
		t.Fatal("too little disk space must be refused with a reason")
	}
	if p := planStream(m, small, true); !p.Fits {
		t.Fatal("a file already on that computer needs no free space")
	}
	linux := pc
	linux.StreamVariant, linux.StreamWhy = "", "not available on Linux yet"
	if p := planStream(m, linux, false); p.Fits {
		t.Fatal("a computer without a package must be refused")
	}
	m.Arch = "llama"
	if p := planStream(m, pc, false); p.Fits {
		t.Fatal("a model family InferSSD does not know must be refused")
	}
	if id, ok := streamChoice("stream:pc"); !ok || id != "pc" {
		t.Fatal("stream choice not recognised")
	}
	if _, ok := streamChoice("pc"); ok {
		t.Fatal("a plain solo choice is not a stream choice")
	}
	for name, want := range map[string]bool{
		"DeepSeek-V4.1-Flash-1017-UD-IQ1_S-00001-of-00005.gguf": true, "Qwen3.5-122B-A10B-UD-IQ2_XXS.gguf": true,
		"Qwen3.8-Flash-Next-IQ4_XS.gguf": true, "Llama-3.3-70B-Q4_K_M.gguf": false, "Qwen3-30B-A3B-Q4_K_M.gguf": false,
	} {
		if streamNamePattern.MatchString(name) != want {
			t.Fatalf("%s: name match %v", name, !want)
		}
	}
}

func TestStreamArgs(t *testing.T) {
	a := strings.Join(streamArgs("/m/x.gguf", 9000, 8192, 20480, true), " ")
	for _, want := range []string{"serve -m /m/x.gguf", "--host 127.0.0.1", "--port 9000", "--context 8192", "host.budget_mib=20480", "vram.budget=0"} {
		if !strings.Contains(a, want) {
			t.Fatalf("%q missing from %q", want, a)
		}
	}
}

func TestContextBytesKeyQ8(t *testing.T) {
	m := &ModelInfo{Layers: 4, KVHeads: []int{8}, KeyLen: 128, ValLen: 128}
	full := m.ContextTotal(8192)
	m.KVKeyQ8 = true
	sq := m.ContextTotal(8192)
	if !(sq < full && float64(sq)/float64(full) > 0.7 && float64(sq)/float64(full) < 0.8) {
		t.Fatalf("8-bit keys should take about 77%% of the 16-bit cache: %d vs %d", sq, full)
	}
}
