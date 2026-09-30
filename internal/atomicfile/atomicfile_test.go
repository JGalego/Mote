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

func TestWriteFollowsALink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links need privileges on Windows")
	}
	dir := t.TempDir()
	real, link := filepath.Join(dir, "real.md"), filepath.Join(dir, "link.md")
	os.WriteFile(real, []byte("old"), 0o644)
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	if err := Write(link, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(real); string(b) != "new" {
		t.Errorf("the target holds %q", b)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the link was replaced by a file")
	}
}

func TestWriteStatDescribesTheFileWritten(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	fi, err := WriteStat(p, []byte("12345"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	now, _ := os.Stat(p)
	if !Same(fi, now) || fi.Size() != 5 {
		t.Errorf("stat %v vs %v", fi, now)
	}
	if Same(nil, now) || Same(fi, nil) {
		t.Error("Same with nothing")
	}
}

func TestWriteNewDoesNotReplace(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := WriteNew(p, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteNew(p, []byte("second"), 0o644); err == nil {
		t.Error("an existing file was replaced")
	}
	if b, _ := os.ReadFile(p); string(b) != "first" {
		t.Errorf("content %q", b)
	}
	if err := WriteNew(filepath.Join(t.TempDir(), "no", "dir", "f"), nil, 0o644); err == nil {
		t.Error("a missing directory was accepted")
	}
}
