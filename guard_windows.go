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
	commitLow := m.TotalPageFile > 0 && m.AvailPageFile < m.TotalPageFile/50
	physFull := m.MemoryLoad >= 99 && m.AvailPhys < m.TotalPhys/100
	if commitLow || physFull {
		return true, fmt.Sprintf("%s of %s memory free, %s of %s commit left", humanBytes(int64(m.AvailPhys)), humanBytes(int64(m.TotalPhys)), humanBytes(int64(m.AvailPageFile)), humanBytes(int64(m.TotalPageFile)))
	}
	return false, ""
}

func lastResort(pid int) {}
