//go:build windows

package main

import (
	"os"
	"os/exec"
	"strconv"
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

// killTree stops a program and the programs it started (InferSSD's launcher starts its
// server as a child).
func killTree(pid int) {
	c := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	hideWindow(c)
	_ = c.Run()
}

// ownGroup: on Windows killTree follows the program's children by itself.
func ownGroup(cmd *exec.Cmd) { hideWindow(cmd) }

// execSelf starts a new run of Potluck and ends this one.
func execSelf(exe string, args []string) {
	c := exec.Command(exe, args...)
	if c.Start() == nil {
		os.Exit(0)
	}
}

// procPath is the program a running process was started from ("" if unknown).
func procPath(pid int) string {
	h, err := syscall.OpenProcess(0x1000 /* PROCESS_QUERY_LIMITED_INFORMATION */, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer syscall.CloseHandle(h)
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	q := kernel32.NewProc("QueryFullProcessImageNameW")
	if r, _, _ := q.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))); r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}
