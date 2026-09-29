package ui

import (
	"os"
	"testing"
)

func TestEnterCbreakOnNonTerminal(t *testing.T) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if _, ok := EnterCbreak(null); ok {
		t.Error("the null device should not enter cbreak mode")
	}
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	if _, ok := EnterCbreak(r); ok {
		t.Error("a pipe should not enter cbreak mode")
	}
	restore, ok := EnterCbreak(r)
	restore() // must be safe to call even when ok is false
	if ok {
		t.Error("expected ok to stay false")
	}
}
