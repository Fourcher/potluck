package main

import (
	"slices"
	"strings"
	"testing"
)

func TestThreadsFor(t *testing.T) {
	full := Limits{Pedal: 100}
	cases := []struct {
		name string
		in   NodeInfo
		all  bool
		want int
	}{
		{"4 cores 8 threads, all (desktop, measured)", NodeInfo{Cores: 4, Threads: 8, Limits: full}, true, 8},
		{"4 cores 8 threads, per core", NodeInfo{Cores: 4, Threads: 8, Limits: full}, false, 4},
		{"4 fast + 8 slow cores (laptop, measured)", NodeInfo{Cores: 12, Threads: 16, PerfThreads: 8, Limits: full}, true, 8},
		{"no thread count known", NodeInfo{Cores: 6, Limits: full}, true, 6},
		{"half pedal", NodeInfo{Cores: 4, Threads: 8, Limits: Limits{Pedal: 50}}, true, 4},
	}
	for _, c := range cases {
		if got := threadsFor(c.in, c.all); got != c.want {
			t.Errorf("%s: %d threads, want %d", c.name, got, c.want)
		}
	}
}

func TestPlacementArgs(t *testing.T) {
	a := placementArgs(999, []int{0, 1, 22}, true)
	s := strings.Join(a, " ")
	if !strings.Contains(s, `-ot blk\.(0|1|22)\.ffn_[a-z_]*exps=CPU`) || !slices.Contains(a, "q8_0") {
		t.Fatalf("placement flags: %s", s)
	}
	if !slices.Contains(a, "--no-op-offload") {
		t.Fatalf("experts kept in memory must be worked on there (op offload fills memory at 32k): %s", s)
	}
	if s := strings.Join(placementArgs(0, nil, false), " "); s != "-ngl 0" {
		t.Fatalf("processor only: %q", s)
	}
}

func TestSamplingArgs(t *testing.T) {
	// The Inkling files on hand carry only general.sampling.temp; the rest comes from the table.
	m := &ModelInfo{Arch: "inkling2_loop", FileSampling: map[string]bool{"temp": true}}
	if got := strings.Join(samplingArgs(m), " "); got != "--min-p 0 --top-k 0 --top-p 1" {
		t.Fatalf("inkling: %q", got)
	}
	if got := samplingArgs(&ModelInfo{Arch: "llama"}); len(got) != 0 {
		t.Fatalf("unknown family should add nothing: %v", got)
	}
	if got := strings.Join(safeSampling([]string{"--temp", "0.6", "--rpc", "x", "--top-k", "abc"}), " "); got != "--temp 0.6" {
		t.Fatalf("safeSampling: %q", got)
	}
}
