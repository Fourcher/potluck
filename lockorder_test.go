package main

import (
	"sync"
	"testing"
	"time"
)

// The paths that take the host's locks together, run at once: a deadlock hangs this test
// (caught by its own timeout) and a data race shows under -race.
func TestLockOrder(t *testing.T) {
	h := &Host{cfg: loadConfig(t.TempDir()), id: &Identity{FP: "main"}, nodes: map[string]*Node{
		"pc": {Online: true, Info: NodeInfo{Name: "pc", Cores: 4, Threads: 8, RAMTotal: 32 << 30, RAMAvail: 30 << 30, Limits: Limits{Pedal: 100, Enabled: true}}},
	}, crew: map[string]*CrewMember{"c": {Name: "c", Node: "pc", Phase: "running", bytes: 1 << 30}}}
	h.helper = helperState{Phase: "ready", Node: "pc", bytes: 1 << 30}
	m := fakeModel(24, 200<<20, 500<<20)
	var wg sync.WaitGroup
	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(4)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = h.planNodes()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = h.HelperStatus()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = h.CrewList()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = h.planFor(m, "", "pc")
			}
		}()
	}
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("deadlock: the lock paths did not finish")
	}
}
