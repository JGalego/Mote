package ui

import (
	"os"
	"syscall"
	"unsafe"
)

// enableVT turns on ANSI escape processing for a Windows console handle.
func enableVT(f *os.File) bool {
	k := syscall.NewLazyDLL("kernel32.dll")
	var mode uint32
	h := syscall.Handle(f.Fd())
	if r, _, _ := k.NewProc("GetConsoleMode").Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return false
	}
	const vt = 0x0004 // ENABLE_VIRTUAL_TERMINAL_PROCESSING
	r, _, _ := k.NewProc("SetConsoleMode").Call(uintptr(h), uintptr(mode|vt))
	return r != 0
}
