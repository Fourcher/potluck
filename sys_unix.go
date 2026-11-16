//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func sysMem() (total, avail uint64, ok bool) { return 0, 0, false }

func diskFree(path string) uint64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	return uint64(st.Bavail) * uint64(st.Bsize)
}

func lowerPriority(pid int) { _ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, 10) }

func hideWindow(cmd *exec.Cmd) {}
