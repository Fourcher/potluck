//go:build !windows

package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestEnginesStopOnQuitAndAreReapedLater(t *testing.T) {
	dir := t.TempDir()
	reapStale(dir) // sets where the list is kept
	a := &Agent{dir: dir}
	p, err := a.startProc("test", "", "/bin/sleep", []string{"60"}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	p.mu.Lock()
	pid := p.cmd.Process.Pid
	p.mu.Unlock()
	b, _ := os.ReadFile(enginesPath(dir))
	if !strings.Contains(string(b), strconv.Itoa(pid)) {
		t.Fatalf("engine %d not recorded: %q", pid, b)
	}
	killAllEngines()
	time.Sleep(300 * time.Millisecond)
	_ = syscall.Kill(pid, 0)
	if alive(pid) {
		// reaped zombies still answer kill 0 until waited; the proc goroutine waits
		time.Sleep(time.Second)
	}

	// A leftover from a run that died without cleaning up: reaped at the next start.
	c := exec.Command("/bin/sleep", "60")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	ended := make(chan struct{})
	go func() { _ = c.Wait(); close(ended) }()
	_ = os.WriteFile(enginesPath(dir), []byte(strconv.Itoa(c.Process.Pid)+" "+procPath(c.Process.Pid)+"\n"), 0o644)
	reapStale(dir)
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("the leftover engine was not ended")
	}
	if _, err := os.Stat(enginesPath(dir)); err == nil {
		t.Fatal("the old list should be gone")
	}
}
