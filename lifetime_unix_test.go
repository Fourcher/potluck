//go:build !linux && !windows

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "__engine-guard" {
		runEngineGuard(os.Args[2:])
		return
	}
	os.Exit(m.Run())
}

func aliveMarker(marker string) bool {
	out, _ := exec.Command("pgrep", "-f", marker).Output()
	return strings.TrimSpace(string(out)) != ""
}

// An engine started under the watcher ends when Potluck (its parent) ends, even by kill -9.
func TestEngineGuardEndsWithParent(t *testing.T) {
	exe, _ := os.Executable()
	const marker = "61.731"
	// sh plays Potluck: it starts the watcher, then exits without stopping anything.
	sh := exec.Command("/bin/sh", "-c", `"$0" __engine-guard /bin/sleep `+marker+` & sleep 0.5`, exe)
	if err := sh.Run(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if !aliveMarker("sleep " + marker) {
			return
		}
	}
	_ = exec.Command("pkill", "-f", "sleep "+marker).Run()
	t.Fatal("the engine outlived its parent")
}

// Stopping the engine the usual way (ending its group) ends watcher and engine together.
func TestEngineGuardStop(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "62.731")
	tieToParent(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if !aliveMarker("sleep 62.731") {
		t.Fatal("engine did not start under the watcher")
	}
	killTree(cmd.Process.Pid)
	_ = cmd.Wait()
	time.Sleep(300 * time.Millisecond)
	if aliveMarker("sleep 62.731") {
		_ = exec.Command("pkill", "-f", "sleep 62.731").Run()
		t.Fatal("the engine outlived a stop")
	}
}
