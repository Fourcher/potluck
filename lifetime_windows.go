//go:build windows

package main

import (
	"os"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Engines never outlive Potluck: each one joins a Windows job object that ends everything in
// it when Potluck's handle to it closes, which Windows does however Potluck ends (quit, crash,
// Task Manager).
var (
	jobOnce sync.Once
	jobH    windows.Handle
)

func engineJob() windows.Handle {
	jobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
		}
		if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			windows.CloseHandle(h)
			return
		}
		jobH = h
	})
	return jobH
}

func tieToParent(cmd *exec.Cmd) {}

func adoptEngine(pid int) {
	j := engineJob()
	if j == 0 {
		return
	}
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(p)
	_ = windows.AssignProcessToJobObject(j, p)
}

// runEngineGuard is only needed on macOS (lifetime_unix.go).
func runEngineGuard(args []string) { os.Exit(2) }
