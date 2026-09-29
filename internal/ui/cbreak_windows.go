package ui

import (
	"os"
	"syscall"
	"unsafe"
)

// EnterCbreak turns off line input and local echo on f's console, so a
// caller can read one byte at a time and draw its own prompt; Ctrl-C still
// raises its event as usual. The returned func restores the console mode,
// and ok is false when f is not a console cbreak mode can act on.
func EnterCbreak(f *os.File) (restore func(), ok bool) {
	k := syscall.NewLazyDLL("kernel32.dll")
	getMode, setMode := k.NewProc("GetConsoleMode"), k.NewProc("SetConsoleMode")
	h := syscall.Handle(f.Fd())
	var mode uint32
	if r, _, _ := getMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return func() {}, false
	}
	const (
		enableProcessedInput = 0x0001
		enableLineInput      = 0x0002
		enableEchoInput      = 0x0004
	)
	cbreak := mode&^(enableLineInput|enableEchoInput) | enableProcessedInput
	if r, _, _ := setMode.Call(uintptr(h), uintptr(cbreak)); r == 0 {
		return func() {}, false
	}
	return func() { setMode.Call(uintptr(h), uintptr(mode)) }, true
}
