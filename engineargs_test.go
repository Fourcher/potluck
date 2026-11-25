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
		want int
	}{
		{"4 cores 8 threads (desktop, measured)", NodeInfo{Cores: 4, Threads: 8, Limits: full}, 8},
		{"4 fast + 8 slow cores (laptop, measured)", NodeInfo{Cores: 12, Threads: 16, PerfThreads: 8, Limits: full}, 8},
		{"no thread count known", NodeInfo{Cores: 6, Limits: full}, 6},
		{"half pedal", NodeInfo{Cores: 4, Threads: 8, Limits: Limits{Pedal: 50}}, 4},
	}
	for _, c := range cases {
		if got := threadsFor(c.in); got != c.want {
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
	if slices.Contains(a, "--no-op-offload") {
		t.Fatalf("op offload should stay on (measured faster and it fits): %s", s)
	}
	if s := strings.Join(placementArgs(0, nil, false), " "); s != "-ngl 0" {
		t.Fatalf("processor only: %q", s)
	}
}
