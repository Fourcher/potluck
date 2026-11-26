package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// inkling is Inkling 2 Mini's attention shape, from its GGUF header: 24 layers, 8 key/value
// heads of 128, a 1024-token sliding window on 5 of every 6 layers, layers 6-17 run 3 times.
func inkling() *ModelInfo {
	m := &ModelInfo{Layers: 24, KeyLen: 128, ValLen: 128, SWAWindow: 1024, LoopStart: 6, LoopEnd: 18, LoopCount: 3, Vocab: 201024}
	for i := 0; i < 24; i++ {
		m.KVHeads = append(m.KVHeads, 8)
		m.SWALayers = append(m.SWALayers, (i+1)%6 != 0)
	}
	return m
}

func TestContextBytes(t *testing.T) {
	m := inkling()
	// Full-attention passes: layer 5 once, 11 and 17 three times each, 23 once = 8 passes of
	// 32768 tokens. Sliding-window passes: 40 of 1536 tokens (the engine's "1536 cells").
	// Each token of each pass: 8 heads x (128+128) x 2 bytes = 4096 bytes.
	want := int64(8*32768+40*1536) * 4096
	if got := m.ContextTotal(32768); got != want {
		t.Fatalf("32k context: %d bytes, want %d", got, want)
	}
	// Short contexts: the window does not cap anything below 1536 tokens.
	if got, want := m.ContextTotal(1024), int64(48*1024)*4096; got != want {
		t.Fatalf("1k context: %d, want %d", got, want)
	}
	if (&ModelInfo{Layers: 4}).ContextBytes(4096) != nil {
		t.Fatal("a model without an attention shape should give no estimate")
	}
	// The working allowance follows the vocabulary: 201k words x 512 tokens x 4 bytes x 2.
	if got := m.WorkBytes(); got != 201024*512*4*2 {
		t.Fatalf("work bytes %d", got)
	}
}

// A plan for a long context must count its cache: the same computers that hold the model at
// a short context cannot hold it at a long one.
func TestPlanCountsContext(t *testing.T) {
	m := inkling()
	m.LayerBytes = make([]int64, 24)
	for i := range m.LayerBytes {
		m.LayerBytes[i] = 600 << 20
	}
	m.OtherBytes = 1 << 30
	node := PlanNode{ID: "a", Name: "A", CPUBytes: 17 << 30}
	m.PlanCtx = 4096
	if p := planLayout(m, []PlanNode{node}, ""); !p.Fits {
		t.Fatalf("4k context should fit: %s", p.Reason)
	}
	m.PlanCtx = 262144
	if p := planLayout(m, []PlanNode{node}, ""); p.Fits {
		t.Fatalf("256k context (about %s of cache) should not fit in 17 GB", humanBytes(m.ContextTotal(262144)))
	}
}

func TestOOMLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "head.log")
	p := &proc{logPath: path}
	off := map[*proc]int64{}
	_ = os.WriteFile(path, []byte("load_tensors: offloaded 49/49 layers\nsrv  all slots are idle\n"), 0o644)
	if l := newOOMLine(p, off); l != "" {
		t.Fatalf("false alarm on a normal log: %q", l)
	}
	oom := "E error: Insufficient Memory (00000008:kIOGPUCommandBufferCallbackErrorOutOfMemory)\n"
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(oom)
	f.Close()
	if l := newOOMLine(p, off); l == "" {
		t.Fatal("missed the Metal out-of-memory line from the Mac mini crash")
	}
	if l := newOOMLine(p, off); l != "" {
		t.Fatal("reported the same line twice")
	}
	// A new engine at the same path, whose log has already grown past the old one's end
	// before the guard looks: read from its start (found by the self-review).
	_ = os.WriteFile(path, []byte(strings.Repeat("load_tensors: a long line of loading noise\n", 4)+oom), 0o644)
	if l := newOOMLine(&proc{logPath: path}, off); l == "" {
		t.Fatal("missed the new engine's out-of-memory line")
	}
}

func TestCeiling(t *testing.T) {
	h := &Host{cfg: loadConfig(t.TempDir())}
	set := func(c memCeiling) {
		h.cfg.Update(func(cf *Config) { cf.Ceilings = map[string]memCeiling{"a": c} })
	}
	set(memCeiling{Bytes: 10 << 30, Until: time.Now().Add(time.Hour)})
	if c, g := h.capByCeiling("a", 12<<30, 2<<30); c+g != 10<<30 || g != 2<<30 {
		t.Fatalf("cap: cpu %d gpu %d", c, g)
	}
	if c, g := h.capByCeiling("b", 12<<30, 0); c != 12<<30 || g != 0 {
		t.Fatal("a computer that never ran out must not be limited")
	}
	set(memCeiling{Bytes: 1, Until: time.Now().Add(-time.Second)})
	if c, _ := h.capByCeiling("a", 12<<30, 0); c != 12<<30 {
		t.Fatal("an expired ceiling still applies")
	}
}

// The reader gets the real file's shape right (skipped where the file is not on disk).
func TestInklingHeader(t *testing.T) {
	path := os.Getenv("POTLUCK_TEST_INKLING")
	if path == "" {
		t.Skip("set POTLUCK_TEST_INKLING to an Inkling 2 Mini GGUF file")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Skip(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	buf := make([]byte, 32<<20)
	n, _ := f.Read(buf)
	m, err := parseGGUF(buf[:n], st.Size())
	if err != nil {
		t.Fatal(err)
	}
	want := inkling()
	m.PlanCtx = 32768
	if m.ContextTotal(32768) != want.ContextTotal(32768) || m.Vocab != want.Vocab {
		t.Fatalf("read %d bytes of context cache and vocab %d; want %d and %d (heads %v, swa %v, loop %d-%d x%d)",
			m.ContextTotal(32768), m.Vocab, want.ContextTotal(32768), want.Vocab, m.KVHeads, m.SWALayers, m.LoopStart, m.LoopEnd, m.LoopCount)
	}
}

// A pooled looped model: with the split given in the engine's slots, every file layer's weights
// land on the device the plan chose (the engine places them with the slot of the same number),
// and the repeat slots and the output go with the last device.
func TestLoopSplit(t *testing.T) {
	m := inkling()
	m.LayerBytes = make([]int64, 24)
	for i := range m.LayerBytes {
		m.LayerBytes[i] = 1100 << 20
	}
	m.OtherBytes = 1 << 30
	m.PlanCtx = 8192
	// Inkling's real core (6-17) cannot be split between a processor-only head (its layers come
	// first) and a helper (last): refused below. For the slot arithmetic, loop the last six.
	m.LoopStart, m.LoopEnd = 18, 24
	nodes := []PlanNode{{ID: "big", Name: "Big", CPUBytes: 22 << 30}, {ID: "small", Name: "Small", CPUBytes: 12 << 30}}
	p := planLayout(m, nodes, "big")
	// The loop core (layers 6-17) must all be on the last device.
	if p.Fits && len(p.DevLayers) > 0 && m.Layers-p.DevLayers[len(p.DevLayers)-1] > m.LoopStart {
		t.Fatalf("plan splits the loop core across devices: %v", p.DevLayers)
	}
	// Inkling's own core across such a layout: refused with a reason, not loaded broken.
	real := *m
	real.LoopStart, real.LoopEnd = 6, 18
	if q := planLayout(&real, []PlanNode{{ID: "big", Name: "Big", CPUBytes: 14 << 30}, {ID: "a", Name: "A", CPUBytes: 12 << 30}, {ID: "b", Name: "B", CPUBytes: 12 << 30}}, "big"); q.Fits || !strings.Contains(q.Reason, "repeats its layers") {
		t.Fatalf("small head: fits=%v %q %v", q.Fits, q.Reason, q.DevLayers)
	}
	if !p.Fits || len(p.DevLayers) < 1 {
		t.Fatalf("plan: fits=%v %s devs=%v", p.Fits, p.Reason, p.DevLayers)
	}
	var split []float64
	for _, s := range strings.Split(p.TensorSplit, ",") {
		v, _ := strconv.ParseFloat(s, 64)
		split = append(split, v)
	}
	// Which device the plan meant for each file layer, in device order.
	cpu := m.Layers + 1 + m.loopSlots() - p.NGL
	want := []int{}
	for d, n := range p.DevLayers {
		for i := 0; i < n; i++ {
			want = append(want, d)
		}
	}
	for l := cpu; l < m.Layers; l++ {
		if got := deviceOfLayer(l, m.EngineLayers(), p.NGL, split); got != want[l-cpu] {
			t.Fatalf("file layer %d: engine puts it on device %d, plan says %d (split %s, ngl %d)", l, got, want[l-cpu], p.TensorSplit, p.NGL)
		}
	}
	last := len(p.DevLayers) - 1
	for slot := m.Layers; slot <= m.EngineLayers(); slot++ {
		if got := deviceOfLayer(slot, m.EngineLayers(), p.NGL, split); got != last {
			t.Fatalf("repeat/output slot %d on device %d, want the last (%d)", slot, got, last)
		}
	}
}

// The pattern that keeps a layer's experts in memory catches every way models name them,
// and nothing else.
func TestExpertPattern(t *testing.T) {
	re := regexp.MustCompile(`^blk\.(5|6)\.ffn_[a-z_]*exps`)
	for name, want := range map[string]bool{
		"blk.5.ffn_gate_up_exps.weight": true, "blk.5.ffn_down_exps.weight": true, "blk.6.ffn_up_exps.weight": true,
		"blk.6.ffn_gate_exps.weight": true, "blk.5.ffn_down_chexps.weight": true,
		"blk.5.ffn_gate_inp.weight": false, "blk.5.ffn_norm.weight": false, "blk.5.attn_q.weight": false, "blk.7.ffn_down_exps.weight": false,
	} {
		if re.MatchString(name) != want || isExpertTensor(name) != (want || strings.HasPrefix(name, "blk.7.ffn_down_exps")) {
			t.Fatalf("%s: pattern %v, isExpertTensor %v, want %v", name, re.MatchString(name), isExpertTensor(name), want)
		}
	}
}

func TestWorkReported(t *testing.T) {
	log := "sched_reserve: RPC0[127.0.0.1:63435] compute buffer size =   206.20 MiB\nsched_reserve:      CUDA0 compute buffer size =  3358.68 MiB\nsched_reserve:        CPU compute buffer size =   177.27 MiB\n"
	if got, f := workReported(log), 3358.68; got != int64(f*(1<<20)) {
		t.Fatalf("%d", got)
	}
	m := inkling()
	m.WorkSeen = 3358 << 20
	if m.WorkBytes() < 3358<<20 {
		t.Fatal("a measured working memory must replace the estimate")
	}
}

// One computer with a card: a looped model's layer count is given in slots, and the card's
// layers always include the loop core.
func TestSoloLoop(t *testing.T) {
	m := inkling()
	m.LayerBytes = make([]int64, 24)
	m.ExpertBytes = make([]int64, 24)
	for i := range m.LayerBytes {
		m.LayerBytes[i], m.ExpertBytes[i] = 1000<<20, 970<<20
	}
	m.OtherBytes, m.OutputBytes, m.PlanCtx = 1<<30, 500<<20, 8192
	p := planSolo(m, PlanNode{ID: "a", Name: "A", CPUBytes: 26 << 30, GPUBytes: 3 << 30, HasGPU: true})
	if !p.Fits {
		t.Fatal(p.Reason)
	}
	if g := p.Shares[0].GPULayers; g > 0 {
		if m.Layers-g > m.LoopStart {
			t.Fatalf("card holds %d layers: the loop core is split", g)
		}
		if p.NGL != g+1+m.loopSlots() {
			t.Fatalf("ngl %d for %d card layers, want it in slots", p.NGL, g)
		}
	}
}

// An engine that ran out of memory and ended between two checks is still reported, once;
// the guard's own note in the log is not taken for a new one.
func TestGuardReadsEndedEngine(t *testing.T) {
	log := filepath.Join(t.TempDir(), "head.log")
	_ = os.WriteFile(log, []byte("loading\nggml_metal: error: insufficient memory\n"), 0o644)
	p := &proc{name: "head", logPath: log} // not running: it already ended
	g := newGuardScan()
	if line := g.oom(p); !strings.Contains(line, "insufficient memory") {
		t.Fatalf("an engine that ended of it was not reported: %q", line)
	}
	if line := g.oom(p); line != "" {
		t.Fatalf("reported twice: %q", line)
	}
	q := &proc{name: "aux", logPath: filepath.Join(t.TempDir(), "aux.log")}
	_ = os.WriteFile(q.logPath, []byte("[potluck] the guard stopped this engine: it ran out of memory (x)\n"), 0o644)
	if line := g.oom(q); line != "" {
		t.Fatalf("the guard's own note was taken for a new one: %q", line)
	}
}
