// Package sandbox runs a command with limited reach: the working directory
// is writable, the rest of the system is read-only, the home directory is
// hidden, and there is no network unless asked for. mote uses it for shell
// commands a model wrote, where checking the command text can only go so
// far and bounding what it can touch is the real protection.
//
// Linux uses bubblewrap (bwrap), macOS its built-in sandbox-exec. Windows
// has no equivalent that fits, so there is none there, and mote says so.
package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Kind names the mechanism in use.
type Kind string

const (
	None     Kind = ""
	Bwrap    Kind = "bwrap"
	Seatbelt Kind = "sandbox-exec"
)

// Sandbox is a mechanism found on this machine.
type Sandbox struct {
	Kind Kind
	Prog string // path of bwrap or sandbox-exec
}

// Available reports whether commands can be sandboxed.
func (s Sandbox) Available() bool { return s.Kind != None }

// Detect finds a sandbox that works here. Finding the program is not
// enough: bwrap needs unprivileged user namespaces, which some systems and
// most containers switch off, so it is tried on a command that does
// nothing. look finds programs, as exec.LookPath does.
func Detect(ctx context.Context, look func(string) (string, error)) Sandbox {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "linux":
		p, err := look("bwrap")
		if err != nil {
			return Sandbox{}
		}
		probe := exec.CommandContext(ctx, p, "--ro-bind", "/", "/", "--dev", "/dev", "--unshare-all", "--die-with-parent", "true")
		if probe.Run() != nil {
			return Sandbox{}
		}
		return Sandbox{Kind: Bwrap, Prog: p}
	case "darwin":
		p := "/usr/bin/sandbox-exec"
		if _, err := os.Stat(p); err != nil {
			return Sandbox{}
		}
		if exec.CommandContext(ctx, p, "-p", "(version 1)(allow default)", "/usr/bin/true").Run() != nil {
			return Sandbox{}
		}
		return Sandbox{Kind: Seatbelt, Prog: p}
	}
	return Sandbox{}
}

// Describe says in a few words what a sandboxed command can reach.
func (s Sandbox) Describe(dir string, net bool) string {
	if !s.Available() {
		return "not sandboxed"
	}
	n := "no network"
	if net {
		n = "network allowed"
	}
	return "sandboxed with " + string(s.Kind) + ": writes only in " + dir + ", home hidden, " + n
}

// realPath resolves symlinks, which sandbox-exec needs (/tmp is
// /private/tmp on macOS) and bwrap tolerates. A path that cannot be
// resolved is kept as it is.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// Args returns the command line that runs argv inside the sandbox, with
// dir writable and the home directory hidden.
func (s Sandbox) Args(dir, home string, net bool, argv []string) []string {
	dir = realPath(dir)
	if home != "" {
		home = realPath(home)
	}
	switch s.Kind {
	case Bwrap:
		args := []string{s.Prog,
			"--ro-bind", "/", "/",
			"--dev", "/dev", "--proc", "/proc",
			"--tmpfs", "/tmp",
		}
		// An empty directory over home, then the working directory
		// mounted back in, which also works when it lies inside home or
		// /tmp. A working directory that is home itself stays visible.
		if home != "" && home != "/" && dir != home {
			args = append(args, "--tmpfs", home)
		}
		args = append(args, "--bind", dir, dir, "--unshare-all")
		if net {
			args = append(args, "--share-net")
		}
		args = append(args, "--die-with-parent", "--new-session", "--chdir", dir, "--")
		return append(args, argv...)
	case Seatbelt:
		return append([]string{s.Prog, "-p", Profile(dir, home, net)}, argv...)
	}
	return argv
}

// quote writes a string literal for a sandbox profile.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// Profile is the sandbox-exec policy. Later rules win, so it allows
// everything, then takes away writing, reading home and the network, then
// gives back what a command needs: its working directory, temporary
// directories and the standard devices.
func Profile(dir, home string, net bool) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	if !net {
		b.WriteString("(deny network*)\n")
	}
	if home != "" && home != "/" && dir != home {
		b.WriteString("(deny file-read* (subpath " + quote(home) + "))\n")
	}
	b.WriteString("(deny file-write*)\n")
	b.WriteString("(allow file-read* file-write* (subpath " + quote(dir) + "))\n")
	b.WriteString(`(allow file-write* (subpath "/private/tmp") (subpath "/private/var/folders") (literal "/dev/null") (literal "/dev/zero") (literal "/dev/tty") (literal "/dev/stdout") (literal "/dev/stderr") (regex #"^/dev/fd/"))` + "\n")
	// Metadata keeps getcwd and path lookups working through hidden
	// parents of the working directory; it does not list or read them.
	b.WriteString("(allow file-read-metadata)\n")
	return b.String()
}

// Command builds an exec.Cmd for argv, sandboxed when a sandbox is
// available, running in dir either way.
func (s Sandbox) Command(ctx context.Context, dir string, net bool, argv ...string) *exec.Cmd {
	home, _ := os.UserHomeDir()
	full := s.Args(dir, home, net, argv)
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	cmd.Dir = dir
	return cmd
}
