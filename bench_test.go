package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"
)

// The estimate must cover every speed measured on the real computers (November 2026).
func TestEstimateMatchesMeasured(t *testing.T) {
	mono := &Profile{MemGBs: 32.3, Threads: 8, DiskMBs: 443}
	llmos := &Profile{MemGBs: 41.8, Threads: 16}
	mac := &Profile{MemGBs: 28.9, Threads: 12}
	check := func(name string, est []Estimate, route string, low, high float64) {
		t.Helper()
		for _, e := range est {
			if e.Route == route {
				if e.Low > low || e.High < high {
					t.Fatalf("%s: estimate %.1f-%.1f does not cover measured %.1f-%.1f (%s)", name, e.Low, e.High, low, high, e.Basis)
				}
				t.Logf("%s: estimate %.1f-%.1f, measured %.1f-%.1f", name, e.Low, e.High, low, high)
				return
			}
		}
		t.Fatalf("%s: no %s estimate in %+v", name, route, est)
	}
	// Qwen3-30B-A3B Q4_K_M on Monolith's processor alone: 8.0-9.8 words/s; with its 1050 Ti
	// holding the part every word uses, as Potluck runs it: 12.7.
	pc := PlanNode{ID: "mono", Name: "Monolith", CPUBytes: 26 << 30, StreamVariant: "windows-vulkan"}
	f := StoreFile{File: "Qwen3-30B-A3B-Instruct-2507-Q4_K_M.gguf", Size: 18556686752, Verdict: "one"}
	check("30B on Monolith, processor only", estimateFile(f, []PlanNode{pc}, map[string]*Profile{"mono": mono}), "one", 8.0, 9.8)
	card := pc
	card.GPUBytes = 3310 << 20
	check("30B on Monolith with its card", estimateFile(f, []PlanNode{card}, map[string]*Profile{"mono": mono}), "one", 12.3, 13.2)
	// Qwen3-4B on LLMOS (llama-bench): Q4_K_M 12.6, Q2_K 17.4, IQ2_XXS 13.8-15.9.
	lb := PlanNode{ID: "llmos", Name: "LLMOS", CPUBytes: 12 << 30}
	for _, c := range []struct {
		file   string
		size   int64
		lo, hi float64
	}{{"Qwen3-4B.i1-Q4_K_M.gguf", 2497281312, 12.6, 12.6}, {"Qwen3-4B.i1-Q2_K.gguf", 1669500192, 17.4, 17.4}, {"Qwen3-4B.i1-IQ2_XXS.gguf", 1246621472, 13.8, 15.9}} {
		check(c.file+" on LLMOS", estimateFile(StoreFile{File: c.file, Size: c.size, Verdict: "one"}, []PlanNode{lb}, map[string]*Profile{"llmos": llmos}), "one", c.lo, c.hi)
	}
	// The 122B IQ2_XXS pooled over Monolith and LLMOS: 3.6-4.1 words/s with one helper.
	big := StoreFile{File: "Qwen3.5-122B-A10B-UD-IQ2_XXS.gguf", Size: 36637668544, Verdict: "pool"}
	pool := []PlanNode{{ID: "mono", Name: "Monolith", CPUBytes: 26 << 30, GPUBytes: 3310 << 20, LinkMs: 10}, {ID: "llmos", Name: "LLMOS", CPUBytes: 9 << 30, LinkMs: 12}, {ID: "mac", Name: "MacBook", CPUBytes: 11 << 30, LinkMs: 30}}
	check("122B pooled", estimateFile(big, pool, map[string]*Profile{"mono": mono, "llmos": llmos, "mac": mac}), "pool", 2.3, 4.1)
	// The same model streamed with InferSSD from Monolith's SATA SSD: 1.0 words/s.
	big.Verdict = "stream"
	check("122B streamed", estimateFile(big, []PlanNode{pc}, map[string]*Profile{"mono": mono}), "stream", 0.95, 1.06)
}

// Without a profile there is no estimate, and a stream estimate without a drive measurement
// says so instead of guessing.
func TestEstimateNeedsProfile(t *testing.T) {
	pc := PlanNode{ID: "pc", Name: "PC", CPUBytes: 26 << 30, StreamVariant: "windows-vulkan"}
	f := StoreFile{File: "x-Q4_K_M.gguf", Size: 5 << 30, Verdict: "one"}
	if e := estimateFile(f, []PlanNode{pc}, nil); len(e) != 0 {
		t.Fatalf("estimate without a profile: %+v", e)
	}
	f = StoreFile{File: "DeepSeek-V4.1-Flash-UD-IQ1_S-00001-of-00005.gguf", Size: 233 << 30, Verdict: "stream"}
	e := estimateFile(f, []PlanNode{pc}, map[string]*Profile{"pc": {MemGBs: 30, Threads: 8}})
	if len(e) != 1 || e[0].High != 0 || e[0].Basis == "" {
		t.Fatalf("stream without drive speed should give no number but a reason: %+v", e)
	}
}

// The drive test writes its own file when there is no model, reads it past the cache, and
// removes it again.
func TestDiskReadSpeedTempFile(t *testing.T) {
	dir := t.TempDir()
	mbs, note := diskReadSpeed(dir, 1<<40, 1500*time.Millisecond)
	if mbs <= 0 {
		t.Fatalf("no drive speed measured: %q", note)
	}
	if _, err := os.Stat(filepath.Join(dir, ".potluck-drive-test")); !os.IsNotExist(err) {
		t.Fatal("the test file was not removed")
	}
	t.Logf("this computer's drive: %.0f MB/s uncached", mbs)
}

func TestAlignedBuf(t *testing.T) {
	for _, n := range []int{4096, benchBlock} {
		b := alignedBuf(n)
		if len(b) != n || uintptr(unsafe.Pointer(&b[0]))%4096 != 0 {
			t.Fatalf("buffer of %d not aligned", n)
		}
	}
}
