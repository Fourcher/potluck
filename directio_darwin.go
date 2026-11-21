//go:build darwin

package main

import (
	"os"
	"syscall"
)

// openUncached opens a file so that reads (and writes) skip the operating system's cache,
// for measuring the drive itself. macOS: F_NOCACHE.
func openUncached(path string, flag int) (*os.File, error) {
	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		return nil, err
	}
	if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_NOCACHE, 1); e != 0 {
		f.Close()
		return nil, e
	}
	return f, nil
}
