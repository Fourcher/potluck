//go:build linux

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// Engines never outlive Potluck: Linux ends each one the moment the Potluck thread that
// started it goes away, however Potluck ends (quit, crash, kill -9). startProc keeps that
// thread alive for as long as the engine runs.
func tieToParent(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}

func adoptEngine(pid int) {}

// runEngineGuard is only needed on macOS (lifetime_unix.go).
func runEngineGuard(args []string) { os.Exit(2) }
