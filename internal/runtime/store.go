// Package runtime installs and runs local inference backends. Model files and
// runtime binaries live under the user's data directory, never in the
// repository.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"

	"github.com/jgalego/mote/registry"
)

// Store resolves paths under the data directory.
type Store struct{ Dir string }

func (s Store) modelDir(m *registry.Model) string { return filepath.Join(s.Dir, "models", m.ID) }

// FilePath is where a model file is stored.
func (s Store) FilePath(m *registry.Model, f registry.File) string {
	return filepath.Join(s.modelDir(m), f.Name)
}

// Files returns role -> local path for a model.
func (s Store) Files(m *registry.Model) map[string]string {
	out := map[string]string{}
	for _, f := range m.Files {
		out[f.Role] = s.FilePath(m, f)
	}
	return out
}

// Missing returns the files of m that are not present with the right size.
func (s Store) Missing(m *registry.Model) []registry.File {
	var out []registry.File
	for _, f := range m.Files {
		if st, err := os.Stat(s.FilePath(m, f)); err != nil || st.Size() != f.Size {
			out = append(out, f)
		}
	}
	return out
}

// Installed reports whether all files of m are present.
func (s Store) Installed(m *registry.Model) bool { return len(s.Missing(m)) == 0 }

// Pull downloads the missing files of m.
func (s Store) Pull(ctx context.Context, f Fetcher, m *registry.Model) error {
	for _, file := range s.Missing(m) {
		if err := f.Download(ctx, file.URL, s.FilePath(m, file), file.SHA256, file.Size, m.ID+"/"+file.Name); err != nil {
			return err
		}
	}
	return nil
}

// Verify re-hashes all files of m.
func (s Store) Verify(m *registry.Model) error {
	for _, f := range m.Files {
		if err := VerifyFile(s.FilePath(m, f), f.SHA256, f.Size); err != nil {
			return err
		}
	}
	return nil
}

// Remove deletes the local files of m.
func (s Store) Remove(m *registry.Model) error { return os.RemoveAll(s.modelDir(m)) }

// MainBinary is the program whose presence marks a backend's runtime as
// installed.
var MainBinary = map[string]string{"llama.cpp": "llama-server", "sd.cpp": "sd-cli"}

// runtimeName is the backend a runtime serves; older registries left it
// implicit for llama.cpp.
func runtimeName(rt registry.Runtime) string {
	if rt.Name == "" {
		return "llama.cpp"
	}
	return rt.Name
}

// RuntimeDir is where a pinned runtime release is unpacked, one directory
// per backend, version and build variant (a GPU-offloading build, when this
// OS/arch needs a different one from the default, shares neither files nor
// a directory with the default build).
func (s Store) RuntimeDir(rt registry.Runtime, variant string) string {
	name := runtimeName(rt) + "-" + rt.Version
	if variant != "" {
		name += "-" + variant
	}
	return filepath.Join(s.Dir, "runtime", name)
}

// RuntimeInstalled reports whether rt is unpacked, returning its directory.
func (s Store) RuntimeInstalled(rt registry.Runtime, variant string) (string, bool) {
	dir := s.RuntimeDir(rt, variant)
	_, err := os.Stat(filepath.Join(dir, exe(MainBinary[runtimeName(rt)])))
	return dir, err == nil
}

// InstallRuntime downloads, verifies and unpacks a pinned runtime build for
// this OS/arch. It is a no-op when already installed.
func (s Store) InstallRuntime(ctx context.Context, f Fetcher, rt registry.Runtime, a registry.Asset) (string, error) {
	name := runtimeName(rt)
	main := exe(MainBinary[name])
	dir := s.RuntimeDir(rt, a.Variant)
	if _, err := os.Stat(filepath.Join(dir, main)); err == nil {
		return dir, nil
	}
	archive := filepath.Join(s.Dir, "runtime", filepath.Base(a.URL))
	if err := f.Download(ctx, a.URL, archive, a.SHA256, a.Size, name+" "+rt.Version); err != nil {
		return "", err
	}
	tmp := dir + ".tmp"
	os.RemoveAll(tmp)
	if err := Extract(archive, tmp); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	if _, err := os.Stat(filepath.Join(tmp, main)); err != nil {
		os.RemoveAll(tmp)
		return "", fmt.Errorf("%s archive does not contain %s", name, main)
	}
	os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		return "", err
	}
	os.Remove(archive)
	return dir, nil
}

// ErrNoRuntime means no llama.cpp binaries could be found.
var ErrNoRuntime = errors.New("llama.cpp runtime not installed; run `mote setup` (or `mote update`)")

// FindLlama locates the directory holding llama-server: an explicitly
// configured directory first, then the managed install. variant is "" for
// the default CPU-only build, or "gpu" for the one that offloads to a GPU,
// on the platforms that pin a separate build for it.
func FindLlama(configured string, s Store, rt registry.Runtime, variant string) (string, error) {
	for _, d := range []string{configured, s.RuntimeDir(rt, variant)} {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, exe("llama-server"))); err == nil {
			return d, nil
		}
		if configured != "" && d == configured {
			return "", fmt.Errorf("llama_dir %s does not contain %s", d, exe("llama-server"))
		}
	}
	return "", ErrNoRuntime
}

// SystemLlama returns the directory of a llama-server found on PATH, if any.
func SystemLlama() string {
	p, err := exec.LookPath("llama-server")
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Dir(p)
}

func exe(name string) string {
	if goruntime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}
