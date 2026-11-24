//go:build windows

package main

import (
	"fmt"
	"unsafe"
)

// memoryCritical: the commit limit (memory plus page file) is nearly used up, so allocations
// are about to fail; or physical memory is completely full and the system lives off the page
// file.
func memoryCritical() (bool, string) {
	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	if r, _, _ := procGlobalMemoryEx.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return false, ""
	}
	// Windows only stalls or fails when its commit limit (memory plus page file) runs out; with
	// memory full but commit to spare it pages to disk: slow, not frozen. Stopping a long task
	// for that alone was too eager (2026-11-24: a 10-hour review stopped with 13 GB of commit
	// left). So: commit nearly gone, or memory full with little commit left.
	commitLow := m.TotalPageFile > 0 && m.AvailPageFile < m.TotalPageFile/50
	physFull := m.MemoryLoad >= 99 && m.AvailPhys < m.TotalPhys/100 && m.TotalPageFile > 0 && m.AvailPageFile < m.TotalPageFile/10
	if commitLow || physFull {
		return true, fmt.Sprintf("%s of %s memory free, %s of %s commit left", humanBytes(int64(m.AvailPhys)), humanBytes(int64(m.TotalPhys)), humanBytes(int64(m.AvailPageFile)), humanBytes(int64(m.TotalPageFile)))
	}
	return false, ""
}

func lastResort(pid int) {}
