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

// termWidth returns the console's visible column count, or 0 if unknown.
func termWidth(f *os.File) int {
	var info struct {
		Size, Cursor             [2]int16
		Attr                     uint16
		Left, Top, Right, Bottom int16
		MaxX, MaxY               int16
	}
	k := syscall.NewLazyDLL("kernel32.dll")
	if r, _, _ := k.NewProc("GetConsoleScreenBufferInfo").Call(f.Fd(), uintptr(unsafe.Pointer(&info))); r == 0 {
		return 0
	}
	return int(info.Right-info.Left) + 1
}

// IsTerminal reports whether f is a console someone can type into; NUL
// and pipes are not.
func IsTerminal(f *os.File) bool {
	var mode uint32
	r, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleMode").Call(f.Fd(), uintptr(unsafe.Pointer(&mode)))
	return r != 0
}
