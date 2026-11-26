//go:build !linux && !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

// Engines never outlive Potluck. macOS has no "end me when my parent ends" switch, so each
// engine runs under a small watcher: Potluck itself, started as "potluck __engine-guard
// <program> <args>". The watcher leads a process group with the engine in it, passes stop
// signals on, and ends the group as soon as Potluck is gone, however it ended (quit, crash,
// kill -9). Potluck stops an engine by ending the whole group (killTree).
func tieToParent(cmd *exec.Cmd) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd.Args = append([]string{exe, "__engine-guard", cmd.Path}, cmd.Args[1:]...)
	cmd.Path = exe
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func adoptEngine(pid int) {}

// runEngineGuard is the watcher's whole life.
func runEngineGuard(args []string) {
	if len(args) == 0 {
		os.Exit(2)
	}
	parent := os.Getppid()
	// Lead a group of our own (Potluck asks for one; this makes sure), so ending "the group"
	// below can only ever reach this watcher and its engine.
	_ = syscall.Setpgid(0, 0)
	if syscall.Getpgrp() != os.Getpid() {
		fmt.Fprintln(os.Stderr, "[potluck] the engine watcher could not get a process group of its own")
		os.Exit(2)
	}
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, 10) // the engine inherits it
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "[potluck] could not start the engine:", err)
		os.Exit(127)
	}
	endAll := func() {
		_ = syscall.Kill(-syscall.Getpgrp(), syscall.SIGKILL)
		os.Exit(1)
	}
	go func() {
		for range time.Tick(time.Second) {
			if os.Getppid() != parent {
				endAll()
			}
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		<-sig
		_ = cmd.Process.Signal(syscall.SIGTERM)
		time.Sleep(5 * time.Second)
		endAll()
	}()
	err := cmd.Wait()
	if ee, ok := err.(*exec.ExitError); ok {
		os.Exit(ee.ExitCode())
	}
	if err != nil {
		os.Exit(1)
	}
}
