//go:build windows

package main

import (
	"os/exec"
	"syscall"
	"unsafe"
)

var (
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procGlobalMemoryEx   = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetDiskFreeSpace = kernel32.NewProc("GetDiskFreeSpaceExW")
	procOpenProcess      = kernel32.NewProc("OpenProcess")
	procSetPriorityClass = kernel32.NewProc("SetPriorityClass")
	procCloseHandle      = kernel32.NewProc("CloseHandle")
)

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func sysMem() (total, avail uint64, ok bool) {
	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	r, _, _ := procGlobalMemoryEx.Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return 0, 0, false
	}
	return m.TotalPhys, m.AvailPhys, true
}

func diskFree(path string) uint64 {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	var free uint64
	r, _, _ := procGetDiskFreeSpace.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&free)), 0, 0)
	if r == 0 {
		return 0
	}
	return free
}

func lowerPriority(pid int) {
	const processSetInformation = 0x0200
	const belowNormal = 0x00004000
	h, _, _ := procOpenProcess.Call(processSetInformation, 0, uintptr(pid))
	if h == 0 {
		return
	}
	procSetPriorityClass.Call(h, belowNormal)
	procCloseHandle.Call(h)
}

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
