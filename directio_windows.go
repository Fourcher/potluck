//go:build windows

package main

import (
	"os"
	"syscall"
)

// openUncached opens a file so that reads skip the operating system's cache, for measuring
// the drive itself. Windows: FILE_FLAG_NO_BUFFERING, which needs aligned buffers and
// sector-aligned offsets (alignedBuf, 4 MiB blocks).
func openUncached(path string, flag int) (*os.File, error) {
	const noBuffering = 0x20000000
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	access, disp := uint32(syscall.GENERIC_READ), uint32(syscall.OPEN_EXISTING)
	if flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		access |= syscall.GENERIC_WRITE
	}
	if flag&os.O_CREATE != 0 {
		disp = syscall.OPEN_ALWAYS
	}
	if flag&os.O_TRUNC != 0 {
		disp = syscall.CREATE_ALWAYS
	}
	h, err := syscall.CreateFile(p, access, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, disp, syscall.FILE_ATTRIBUTE_NORMAL|noBuffering, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}
