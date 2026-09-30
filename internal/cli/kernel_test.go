package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestJupyterDataDirOn(t *testing.T) {
	env := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i+1 < len(kv); i += 2 {
				if kv[i] == k {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	cases := []struct {
		goos, home string
		env        func(string) string
		want       string
	}{
		{"linux", "/h", env(), filepath.Join("/h", ".local", "share", "jupyter")},
		{"linux", "/h", env("XDG_DATA_HOME", "/x"), filepath.Join("/x", "jupyter")},
		{"darwin", "/h", env(), filepath.Join("/h", "Library", "Jupyter")},
		{"windows", `C:\u`, env("APPDATA", `C:\u\Roaming`), filepath.Join(`C:\u\Roaming`, "jupyter")},
		{"windows", `C:\u`, env(), filepath.Join(`C:\u`, "AppData", "Roaming", "jupyter")},
		// Jupyter's own override wins everywhere.
		{"linux", "/h", env("JUPYTER_DATA_DIR", "/j", "XDG_DATA_HOME", "/x"), "/j"},
		{"windows", `C:\u`, env("JUPYTER_DATA_DIR", "/j", "APPDATA", "a"), "/j"},
	}
	for _, c := range cases {
		if got := jupyterDataDirOn(c.goos, c.home, c.env); got != c.want {
			t.Errorf("%s: got %q want %q", c.goos, got, c.want)
		}
	}
}

func TestKernelInstallWritesASpecJupyterCanRead(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	kdir := filepath.Join(dir, "kernels", "mote")

	code, out, errs := e.mote("", "kernel", "path", "--dir", dir)
	if code != 0 || strings.TrimSpace(out) != kdir {
		t.Fatalf("path: %d %q %s", code, out, errs)
	}
	code, _, errs = e.mote("", "kernel", "install", "--dir", dir, "--python", "/opt/py/bin/python")
	if code != 0 || !strings.Contains(errs, "ipykernel") || !strings.Contains(errs, kdir) {
		t.Fatalf("install: %d %s", code, errs)
	}

	var spec kernelSpec
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(kdir, "kernel.json"))), &spec); err != nil {
		t.Fatalf("kernel.json: %v", err)
	}
	script := filepath.Join(kdir, "mote_kernel.py")
	wantArgv := []string{"/opt/py/bin/python", script, "-f", "{connection_file}"}
	if strings.Join(spec.Argv, "|") != strings.Join(wantArgv, "|") {
		t.Errorf("argv %q, want %q", spec.Argv, wantArgv)
	}
	if spec.DisplayName != "mote" || spec.Language != "mote" || spec.InterruptMode != "signal" {
		t.Errorf("spec %+v", spec)
	}
	if spec.Env["MOTE_BIN"] == "" || !filepath.IsAbs(spec.Env["MOTE_BIN"]) {
		t.Errorf("the kernel would not know which mote to run: %v", spec.Env)
	}
	got, err := os.ReadFile(script)
	if err != nil || !bytes.Equal(got, kernelSource) || len(got) == 0 {
		t.Errorf("installed script differs from the embedded one (%v)", err)
	}

	// Installing again replaces it, and a different Python is picked up.
	if code, _, errs := e.mote("", "kernel", "install", "--dir", dir, "--python", "/other/python"); code != 0 {
		t.Fatalf("reinstall: %d %s", code, errs)
	}
	json.Unmarshal([]byte(readFile(t, filepath.Join(kdir, "kernel.json"))), &spec)
	if spec.Argv[0] != "/other/python" {
		t.Errorf("argv after reinstall %q", spec.Argv)
	}

	code, _, errs = e.mote("", "kernel", "uninstall", "--dir", dir)
	if code != 0 {
		t.Fatalf("uninstall: %d %s", code, errs)
	}
	if _, err := os.Stat(kdir); err == nil {
		t.Error("uninstall left the kernel behind")
	}
	if code, _, errs := e.mote("", "kernel", "uninstall", "--dir", dir); code == 0 || !strings.Contains(errs, "not installed") {
		t.Errorf("uninstall of nothing: %d %s", code, errs)
	}
}

func TestKernelInstallDefaultsToAPythonOnThePath(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	// newEnv empties PATH, so without --python nothing can be found and the
	// name alone is written, for Jupyter to resolve.
	if code, _, errs := e.mote("", "kernel", "install", "--dir", dir); code != 0 {
		t.Fatalf("install: %d %s", code, errs)
	}
	var spec kernelSpec
	json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "kernels", "mote", "kernel.json"))), &spec)
	if !strings.HasPrefix(filepath.Base(spec.Argv[0]), "python") {
		t.Errorf("interpreter %q", spec.Argv[0])
	}
}

func TestKernelUsage(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{
		{"kernel"},
		{"kernel", "frobnicate"},
		{"kernel", "install", "extra"},
		{"kernel", "install", "--bogus"},
	} {
		if code, _, _ := e.mote("", args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want usage", args, code)
		}
	}
	// It needs no configuration: nothing here has run `mote setup`.
	if code, _, _ := e.mote("", "kernel", "--help"); code != 0 {
		t.Errorf("--help: exit %d", code)
	}
}

// The parts of the kernel that do not need ipykernel are tested in Python,
// where they run; this runs those tests wherever there is a Python.
func TestKernelPythonTests(t *testing.T) {
	var python string
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			python = p
			break
		}
	}
	if python == "" {
		t.Skip("no Python on the PATH")
	}
	cmd := exec.Command(python, "-B", "-m", "unittest", "discover", "-s", "kernel", "-p", "test_*.py")
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("kernel tests failed: %v\n%s", err, out)
	}
}
