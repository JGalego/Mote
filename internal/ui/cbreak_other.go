//go:build !windows && !linux && !darwin

package ui

import "os"

// EnterCbreak is unsupported on this platform: callers fall back to
// reading whole lines the terminal already edits for them.
func EnterCbreak(*os.File) (restore func(), ok bool) { return func() {}, false }
