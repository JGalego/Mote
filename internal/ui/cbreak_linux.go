package ui

import (
	"os"
	"syscall"
	"unsafe"
)

// EnterCbreak turns off canonical line editing and local echo on f, so a
// caller can read one byte at a time and draw its own prompt; Ctrl-C and
// Ctrl-Z still raise their signals as usual. The returned func restores the
// terminal, and ok is false when f is not a terminal cbreak mode can act on.
func EnterCbreak(f *os.File) (restore func(), ok bool) {
	var t syscall.Termios
	if cbreakIoctl(f, syscall.TCGETS, unsafe.Pointer(&t)) != nil {
		return func() {}, false
	}
	orig := t
	t.Lflag &^= syscall.ICANON | syscall.ECHO
	t.Cc[syscall.VMIN] = 1
	t.Cc[syscall.VTIME] = 0
	if cbreakIoctl(f, syscall.TCSETS, unsafe.Pointer(&t)) != nil {
		return func() {}, false
	}
	return func() { cbreakIoctl(f, syscall.TCSETS, unsafe.Pointer(&orig)) }, true
}

func cbreakIoctl(f *os.File, req uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}
