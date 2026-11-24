//go:build linux

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// memoryCritical: almost no memory left to hand out, or tasks spending much of their time
// stalled waiting for memory (pressure stall information, "full": every task waiting at once).
func memoryCritical() (bool, string) {
	var total, avail uint64
	for _, line := range strings.Split(readFile("/proc/meminfo"), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			total = v << 10
		case "MemAvailable:":
			avail = v << 10
		}
	}
	stall := -1.0
	for _, line := range strings.Split(readFile("/proc/pressure/memory"), "\n") {
		if strings.HasPrefix(line, "full ") {
			for _, kv := range strings.Fields(line) {
				if v, ok := strings.CutPrefix(kv, "avg10="); ok {
					stall, _ = strconv.ParseFloat(v, 64)
				}
			}
		}
	}
	low := total > 0 && avail < max(total/64, 192<<20)
	stalled := stall >= 30
	if low || stalled {
		return true, fmt.Sprintf("%s of %s free, all tasks stalled on memory %.0f%% of the time", humanBytes(int64(avail)), humanBytes(int64(total)), max(stall, 0))
	}
	return false, ""
}

// lastResort makes the kernel pick this engine first if it ever has to end a program to free
// memory, so it never chooses the remote login, the desktop or Potluck itself.
func lastResort(pid int) {
	_ = os.WriteFile("/proc/"+strconv.Itoa(pid)+"/oom_score_adj", []byte("1000"), 0o644)
}
