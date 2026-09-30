package cli

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// `mote kernel install` registers a Jupyter kernel, so a notebook in Jupyter
// or VS Code can take a mote task in each cell and show its output below it.
// The kernel is a small Python program, kernel/mote_kernel.py, that runs
// `mote nb exec` for each cell; installing it is writing that file and a
// kernelspec pointing at it into Jupyter's data directory, which needs no
// Jupyter to be present.

//go:embed kernel/mote_kernel.py
var kernelSource []byte

const (
	kernelName  = "mote"
	kernelUsage = "usage: mote kernel install [--python PATH] [--dir DIR] | uninstall [--dir DIR] | path [--dir DIR]"
)

// jupyterDataDirOn is where Jupyter looks for kernels and other data. It
// takes the operating system and the environment as arguments so each
// platform's location can be checked from any of them.
func jupyterDataDirOn(goos, home string, getenv func(string) string) string {
	if d := getenv("JUPYTER_DATA_DIR"); d != "" {
		return d
	}
	switch goos {
	case "windows":
		if d := getenv("APPDATA"); d != "" {
			return filepath.Join(d, "jupyter")
		}
		return filepath.Join(home, "AppData", "Roaming", "jupyter")
	case "darwin":
		return filepath.Join(home, "Library", "Jupyter")
	default:
		if d := getenv("XDG_DATA_HOME"); d != "" {
			return filepath.Join(d, "jupyter")
		}
		return filepath.Join(home, ".local", "share", "jupyter")
	}
}

// kernelSpec is the kernel.json Jupyter reads.
type kernelSpec struct {
	Argv          []string          `json:"argv"`
	DisplayName   string            `json:"display_name"`
	Language      string            `json:"language"`
	InterruptMode string            `json:"interrupt_mode"`
	Env           map[string]string `json:"env"`
}

// defaultPython is the interpreter the kernel runs under when none is given:
// the first of python3 and python on the PATH. It has to be one that has
// ipykernel, which is why --python exists.
func defaultPython() string {
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	if runtime.GOOS == "windows" {
		return "python"
	}
	return "python3"
}

func (a *app) kernelCmd(args []string) error {
	vals, pos, err := flags(args, []string{"--python", "--dir"}, nil)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("%s", kernelUsage)
	}
	dir := vals["--dir"]
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = jupyterDataDirOn(runtime.GOOS, home, os.Getenv)
	}
	// Jupyter starts a kernel from the notebook's folder, so the paths in its
	// spec must not depend on where this ran.
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	kdir := filepath.Join(dir, "kernels", kernelName)
	switch pos[0] {
	case "path":
		fmt.Fprintln(a.out, kdir)
		return nil
	case "uninstall":
		if _, err := os.Stat(filepath.Join(kdir, "kernel.json")); err != nil {
			return fmt.Errorf("the mote kernel is not installed in %s", kdir)
		}
		if err := os.RemoveAll(kdir); err != nil {
			return err
		}
		fmt.Fprintf(a.err, "%s removed %s\n", a.ue.OK(), kdir)
		return nil
	case "install":
		return a.kernelInstall(kdir, vals["--python"])
	}
	return usagef("%s", kernelUsage)
}

func (a *app) kernelInstall(kdir, python string) error {
	if python == "" {
		python = defaultPython()
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	script := filepath.Join(kdir, "mote_kernel.py")
	spec, err := json.MarshalIndent(kernelSpec{
		Argv:          []string{python, script, "-f", "{connection_file}"},
		DisplayName:   "mote",
		Language:      "mote",
		InterruptMode: "signal",
		Env:           map[string]string{"MOTE_BIN": exe},
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(kdir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(script, kernelSource, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(kdir, "kernel.json"), append(spec, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(a.err, "%s installed the mote kernel in %s\n", a.ue.OK(), kdir)
	fmt.Fprintf(a.err, "%s\n", a.ue.Dim(fmt.Sprintf("it runs under %s, which needs ipykernel (%s -m pip install ipykernel); pick \"mote\" as the kernel of a notebook", python, python)))
	return nil
}
