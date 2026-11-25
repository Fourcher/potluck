package main

import (
	"testing"
	"time"
)

// localPort for a crew slot must not wait on itself (found 2026-11-24: it took a.mu twice and
// hung the helper computer's whole agent).
func TestLocalPortAuxDoesNotDeadlock(t *testing.T) {
	a := &Agent{aux: map[string]*auxEngine{}}
	done := make(chan struct{})
	go func() {
		_, _ = a.localPort("aux:crew-x")
		_, _ = a.localPort("aux")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("localPort deadlocked on an aux target")
	}
}

// Stopping the main model leaves an extra engine's download alone.
func TestStopKeepsAuxJobs(t *testing.T) {
	a := &Agent{}
	mainJob := a.jobCtxFor("model:big.gguf", "")
	crewJob := a.jobCtxFor("model:small.gguf", "aux:crew")
	a.Stop()
	if mainJob.Err() == nil {
		t.Fatal("the main model's download should stop with it")
	}
	if crewJob.Err() != nil {
		t.Fatal("a crew member's download was cancelled by stopping the main model")
	}
}
