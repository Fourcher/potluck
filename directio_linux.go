//go:build linux

package main

import (
	"os"
	"syscall"
)

// openUncached opens a file so that reads skip the operating system's cache, for measuring
// the drive itself. Linux: O_DIRECT, which needs aligned buffers and offsets (alignedBuf).
func openUncached(path string, flag int) (*os.File, error) {
	return os.OpenFile(path, flag|syscall.O_DIRECT, 0o644)
}
