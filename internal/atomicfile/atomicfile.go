// Package atomicfile replaces a file in one step, so that a reader, or a
// crash, never sees it half written.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write replaces path with data. A file that exists keeps its mode; a new
// one gets mode. A path that is a symbolic link has the file it points to
// replaced, and stays a link.
func Write(path string, data []byte, mode os.FileMode) error {
	_, err := WriteStat(path, data, mode)
	return err
}

// WriteStat is Write, and returns what the new file is on disk as it was
// written: its modification time and size, for a caller that watches the file
// to tell its own write from another program's, even one that follows at once.
func WriteStat(path string, data []byte, mode os.FileMode) (os.FileInfo, error) {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return nil, err
	}
	name := tmp.Name()
	fail := func(err error) (os.FileInfo, error) {
		os.Remove(name)
		return nil, err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Chmod(name, mode); err != nil {
		return fail(err)
	}
	// Renaming keeps the time and the size, so these are the file's own.
	fi, err := os.Stat(name)
	if err != nil {
		return fail(err)
	}
	if err := os.Rename(name, path); err != nil {
		return fail(err)
	}
	return fi, nil
}

// WriteNew creates path with data, and fails if something is already there,
// even if it appeared a moment ago.
func WriteNew(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

// Same reports whether two descriptions of a file are of the same version of
// it, as far as its time and size tell.
func Same(a, b os.FileInfo) bool {
	return a != nil && b != nil && a.ModTime().Equal(b.ModTime()) && a.Size() == b.Size()
}
