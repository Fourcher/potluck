package main

import (
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// Engines never outlive Potluck. Each engine process Potluck starts is written to
// <data>/logs/engines.pid. When Potluck is told to quit (the system, a terminal's Ctrl-C,
// Activity Monitor's Quit) it stops them first; and at start it ends any that a run which
// crashed or was killed outright left behind, so their memory comes back without a restart.
// Found on 2026-11-24: a test Potluck stopped with a plain kill left three engines (10 GB)
// running on a laptop.

var (
	enginesMu   sync.Mutex
	enginesFile string
	engines     = map[int]string{} // pid -> program path
)

func enginesPath(dataDir string) string { return filepath.Join(dataDir, "logs", "engines.pid") }

// reapStale ends engines left behind by an earlier run with this data folder. A pid is only
// killed when it still runs the same program file, so a reused pid is never touched.
func reapStale(dataDir string) {
	enginesMu.Lock()
	enginesFile = enginesPath(dataDir)
	enginesMu.Unlock()
	b, err := os.ReadFile(enginesPath(dataDir))
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		pidS, path, ok := strings.Cut(strings.TrimSpace(line), " ")
		pid, err := strconv.Atoi(pidS)
		if !ok || err != nil || pid <= 1 || pid == os.Getpid() {
			continue
		}
		if procPath(pid) == path {
			killTree(pid)
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Kill()
			}
		}
	}
	_ = os.Remove(enginesPath(dataDir))
}

func noteEngine(pid int, path string) {
	enginesMu.Lock()
	defer enginesMu.Unlock()
	engines[pid] = path
	saveEnginesLocked()
}

func forgetEngine(pid int) {
	enginesMu.Lock()
	defer enginesMu.Unlock()
	delete(engines, pid)
	saveEnginesLocked()
}

func saveEnginesLocked() {
	if enginesFile == "" {
		return
	}
	var b strings.Builder
	for pid, path := range engines {
		b.WriteString(strconv.Itoa(pid) + " " + path + "\n")
	}
	_ = os.MkdirAll(filepath.Dir(enginesFile), 0o755)
	_ = os.WriteFile(enginesFile, []byte(b.String()), 0o644)
}

// killAllEngines ends every engine this run started.
func killAllEngines() {
	enginesMu.Lock()
	pids := make([]int, 0, len(engines))
	for pid := range engines {
		pids = append(pids, pid)
	}
	enginesMu.Unlock()
	for _, pid := range pids {
		killTree(pid)
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
		forgetEngine(pid)
	}
}

// stopEnginesOnQuit makes a quit request stop the engines before Potluck ends.
func stopEnginesOnQuit() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-ch
		killAllEngines()
		os.Exit(0)
	}()
}
