package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// On Apple Silicon the graphics chip shares the RAM, but macOS lets it hold only part of it
// (Metal's "recommended maximum working set"; 12124 MiB of 16384 on a 16 GB M4). Loading more
// than that on the graphics chip runs it out of memory, and on 2026-11-23 that froze the
// window server of a Mac mini until the watchdog restarted it. So plans use the real limit.

var metalMu sync.Mutex
var metalMiB int64       // the engine's own figure, once known
var metalTried time.Time // when the engine was last asked

// metalLimit is how much memory the graphics chip may hold on this Mac: a limit set by hand
// with "sysctl iogpu.wired_limit_mb" if there is one, else the engine's own report, else
// 74% of the RAM (what macOS allowed on the 16 GB M4 measured above).
func metalLimit(ram uint64) uint64 {
	if v, err := strconv.ParseUint(strings.TrimSpace(run(2*time.Second, "sysctl", "-n", "iogpu.wired_limit_mb")), 10, 64); err == nil && v > 0 {
		return v << 20
	}
	metalMu.Lock()
	m := metalMiB
	metalMu.Unlock()
	if m > 0 {
		return uint64(m) << 20
	}
	return ram / 100 * 74
}

// probeMetal asks an installed engine for the graphics chip's limit ("MTL0: Apple M4 (12124
// MiB, ...)"). It looks at most every ten minutes until it has an answer.
func probeMetal(dataDir, enginePath string) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return
	}
	metalMu.Lock()
	if metalMiB > 0 || time.Since(metalTried) < 10*time.Minute {
		metalMu.Unlock()
		return
	}
	metalTried = time.Now()
	metalMu.Unlock()
	var bins []string
	if enginePath != "" && enginePath != "potluck" {
		bins = append(bins, enginePath)
	}
	if dirs, _ := filepath.Glob(filepath.Join(dataDir, "runtime", "*")); len(dirs) > 0 {
		for _, d := range dirs {
			if b := findBin(d, "llama-server"); b != "" {
				bins = append(bins, b)
			}
		}
	}
	for _, b := range bins {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		out, _ := exec.CommandContext(ctx, b, "--list-devices").CombinedOutput()
		cancel()
		for _, m := range deviceLine.FindAllStringSubmatch(string(out), -1) {
			if strings.HasPrefix(m[1], "MTL") {
				if v, err := strconv.ParseInt(m[3], 10, 64); err == nil && v > 0 {
					metalMu.Lock()
					metalMiB = v
					metalMu.Unlock()
					return
				}
			}
		}
	}
}
