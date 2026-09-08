//go:build linux

package main

import (
	"os"
	"syscall"
	"unsafe"
)

func quickstartIsTerminal(file *os.File) bool {
	var state syscall.Termios
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&state)))
	return err == 0
}
