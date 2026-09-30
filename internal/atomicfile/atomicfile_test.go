package atomicfile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteReplacesAndKeepsTheMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := Write(p, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Errorf("a new file has mode %v, want the one given", fi.Mode().Perm())
		}
	}
	if err := Write(p, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	fi, _ := os.Stat(p)
	if string(b) != "second" {
		t.Errorf("content %q", b)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("a file that existed changed mode to %v", fi.Mode().Perm())
	}
	if left, _ := filepath.Glob(p + ".tmp*"); len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
	if err := Write(filepath.Join(t.TempDir(), "no", "such", "dir"), nil, 0o644); err == nil {
		t.Error("writing into a missing directory succeeded")
	}
}
