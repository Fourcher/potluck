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
