package main

import (
	"os"
	"path/filepath"
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
	p := filepath.Join(dir, "head.log")
	off := map[string]int64{}
	_ = os.WriteFile(p, []byte("load_tensors: offloaded 49/49 layers\nsrv  all slots are idle\n"), 0o644)
	if l := newOOMLine(p, off); l != "" {
		t.Fatalf("false alarm on a normal log: %q", l)
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("E error: Insufficient Memory (00000008:kIOGPUCommandBufferCallbackErrorOutOfMemory)\n")
	f.Close()
	if l := newOOMLine(p, off); l == "" {
		t.Fatal("missed the Metal out-of-memory line from the Mac mini crash")
	}
	if l := newOOMLine(p, off); l != "" {
		t.Fatal("reported the same line twice")
	}
}

func TestCeiling(t *testing.T) {
	h := &Host{memCeil: map[string]memCeiling{"a": {Bytes: 10 << 30, Until: time.Now().Add(time.Hour)}}}
	if c, g := h.capByCeiling("a", 12<<30, 2<<30); c+g != 10<<30 || g != 2<<30 {
		t.Fatalf("cap: cpu %d gpu %d", c, g)
	}
	if c, g := h.capByCeiling("b", 12<<30, 0); c != 12<<30 || g != 0 {
		t.Fatal("a computer that never ran out must not be limited")
	}
	h.memCeil["a"] = memCeiling{Bytes: 1, Until: time.Now().Add(-time.Second)}
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
