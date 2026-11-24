//go:build darwin

package main

import "syscall"

// memoryCritical: the system's own memory pressure level is "critical" (4). That is the level
// at which macOS starts ending apps; past it the window server can stall.
func memoryCritical() (bool, string) {
	lvl, err := syscall.SysctlUint32("kern.memorystatus_vm_pressure_level")
	if err == nil && lvl >= 4 {
		return true, "macOS reports critical memory pressure"
	}
	return false, ""
}

func lastResort(pid int) {}
