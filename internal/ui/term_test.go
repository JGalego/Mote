package ui

import (
	"os"
	"testing"
)

func TestNullAndFilesAreNotTerminals(t *testing.T) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if IsTerminal(null) {
		t.Error("the null device counted as a terminal")
	}
	f, err := os.CreateTemp(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if IsTerminal(f) {
		t.Error("a file counted as a terminal")
	}
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	if IsTerminal(r) {
		t.Error("a pipe counted as a terminal")
	}
}
