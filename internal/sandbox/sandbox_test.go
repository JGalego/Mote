package sandbox

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The test binary is also the program run inside the sandbox, so the checks
// need nothing installed. MOTE_SANDBOX_HELPER picks what it tries.
func TestMain(m *testing.M) {
	switch os.Getenv("MOTE_SANDBOX_HELPER") {
	case "":
		os.Exit(m.Run())
	case "write":
		if err := os.WriteFile(os.Args[1], []byte("x"), 0o644); err != nil {
			fmt.Println("denied:", err)
			os.Exit(1)
		}
	case "read":
		if _, err := os.ReadFile(os.Args[1]); err != nil {
			fmt.Println("denied:", err)
			os.Exit(1)
		}
	case "net":
		// A listener on the host is reachable only with the network shared.
		c, err := net.DialTimeout("tcp", os.Args[1], 2*time.Second)
		if err != nil {
			fmt.Println("denied:", err)
			os.Exit(1)
		}
		c.Close()
	}
	os.Exit(0)
}

func TestBwrapArgs(t *testing.T) {
	s := Sandbox{Kind: Bwrap, Prog: "/usr/bin/bwrap"}
	got := strings.Join(s.Args("/work", "/home/me", "/tmp", false, []string{"sh", "-c", "ls"}), " ")
	want := "/usr/bin/bwrap --ro-bind / / --dev /dev --proc /proc --tmpfs /tmp --tmpfs /home/me --bind /work /work --unshare-all --die-with-parent --new-session --chdir /work -- sh -c ls"
	if got != want {
		t.Errorf("args\n got %s\nwant %s", got, want)
	}
	// The network is shared only when asked for, and a working directory
	// that is home itself is not hidden from itself.
	got = strings.Join(s.Args("/home/me", "/home/me", "/tmp", true, []string{"ls"}), " ")
	if strings.Contains(got, "--tmpfs /home/me") || !strings.Contains(got, "--unshare-all --share-net") {
		t.Errorf("args %s", got)
	}
	// Without a sandbox, the command is unchanged.
	if got := (Sandbox{}).Args("/w", "/h", "", false, []string{"ls", "-l"}); strings.Join(got, " ") != "ls -l" {
		t.Errorf("no sandbox: %q", got)
	}
}

func TestProfile(t *testing.T) {
	p := Profile("/Users/me/work", "/Users/me", "/private/var/folders/x/T/mote-sandbox-1", false)
	order := []string{
		"(allow default)",
		"(deny network*)",
		`(deny file-read* (subpath "/Users/me"))`,
		"(deny file-write*)",
		`(allow file-read* file-write* (subpath "/Users/me/work"))`,
		`(allow file-read* file-write* (subpath "/private/var/folders/x/T/mote-sandbox-1"))`,
		"(allow file-read-metadata)",
	}
	last := -1
	for _, rule := range order {
		i := strings.Index(p, rule)
		if i < 0 || i < last {
			t.Fatalf("rule %s missing or out of order in\n%s", rule, p)
		}
		last = i
	}
	// The shared temporary areas are not writable, only the command's own.
	if strings.Contains(p, `"/private/tmp"`) || strings.Contains(p, `(subpath "/private/var/folders")`) {
		t.Errorf("shared temporary areas are writable:\n%s", p)
	}
	if strings.Contains(Profile("/w", "/h", "", true), "network") {
		t.Error("network denied although allowed")
	}
	if q := quote(`a "b" \c`); q != `"a \"b\" \\c"` {
		t.Errorf("quote %s", q)
	}
}

func TestDescribe(t *testing.T) {
	if got := (Sandbox{}).Describe("/w", false); got != "not sandboxed" {
		t.Error(got)
	}
	got := Sandbox{Kind: Bwrap}.Describe("/w", false)
	if !strings.Contains(got, "bwrap") || !strings.Contains(got, "/w") || !strings.Contains(got, "no network") {
		t.Error(got)
	}
}

func TestDetectWithoutTheProgram(t *testing.T) {
	missing := func(string) (string, error) { return "", exec.ErrNotFound }
	if s := Detect(context.Background(), missing); s.Available() && s.Kind == Bwrap {
		t.Error("bwrap detected without bwrap")
	}
}

// run runs the test binary in the sandbox as helper mode with one argument.
func run(t *testing.T, s Sandbox, dir string, net bool, mode, arg string) (string, error) {
	t.Helper()
	// The test binary sits under /tmp, which the sandbox replaces with an
	// empty one; a copy in the working directory stays visible.
	self := filepath.Join(dir, "helper"+filepath.Ext(os.Args[0]))
	if _, err := os.Stat(self); err != nil {
		src, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(self, b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd, done := s.Command(ctx, dir, net, self, arg)
	defer done()
	cmd.Env = append(cmd.Environ(), "MOTE_SANDBOX_HELPER="+mode)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestCommandDoesNotLeakTheFullEnvironment guards against a sandboxed
// command reading a secret through its environment: bwrap and sandbox-exec
// bound files and network, not env vars, so Command must build its own
// minimal one rather than passing the parent's through.
func TestCommandDoesNotLeakTheFullEnvironment(t *testing.T) {
	t.Setenv("MOTE_TEST_SECRET", "leak-me")
	s := Sandbox{Kind: Bwrap, Prog: "/usr/bin/bwrap"}
	cmd, done := s.Command(context.Background(), "/work", false, "true")
	defer done()
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "MOTE_TEST_SECRET=") {
			t.Fatalf("sandboxed command inherited an unrelated environment variable: %s", e)
		}
	}
	has := func(prefix string) bool {
		for _, e := range cmd.Env {
			if strings.HasPrefix(e, prefix) {
				return true
			}
		}
		return false
	}
	if !has("PATH=") || !has("HOME=") || !has("TMPDIR=") {
		t.Errorf("sandboxed command is missing PATH, HOME or TMPDIR: %v", cmd.Env)
	}
}

func TestTheSandboxHolds(t *testing.T) {
	s := Detect(context.Background(), exec.LookPath)
	if !s.Available() {
		t.Skip("no working sandbox on this machine")
	}
	// A fake home keeps the real one out of the test. It must not sit in
	// a temporary directory, which the sandbox hides or allows anyway and
	// would make the checks below pass for the wrong reason.
	home, err := os.MkdirTemp(".", "fakehome-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	if home, err = filepath.Abs(home); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	secret := filepath.Join(home, "secret")
	os.WriteFile(secret, []byte("s"), 0o600)
	dir := t.TempDir()

	if out, err := run(t, s, dir, false, "write", filepath.Join(dir, "made")); err != nil {
		t.Fatalf("writing in the working directory failed: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "made")); err != nil {
		t.Error("a write in the working directory did not persist")
	}
	if out, err := run(t, s, dir, false, "read", secret); err == nil {
		t.Errorf("home was readable: %s", out)
	}
	// Writing outside the working directory either fails or lands in a
	// throwaway layer; either way nothing reaches the real disk.
	// Nor anywhere else outside it: a directory that is neither home nor
	// temporary must stay read-only.
	outside, err := os.MkdirTemp(".", "outside-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(outside) })
	outside, _ = filepath.Abs(outside)
	// A shared temporary directory is outside too: on Linux the sandbox
	// sees a private /tmp, on macOS it may write only its own.
	shared := t.TempDir()
	for _, escape := range []string{filepath.Join(home, "escaped"), filepath.Join(outside, "escaped"), filepath.Join(shared, "escaped")} {
		run(t, s, dir, false, "write", escape)
		if _, err := os.Stat(escape); err == nil {
			t.Errorf("a write to %s reached the disk", escape)
		}
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot listen for the network check")
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if out, err := run(t, s, dir, false, "net", ln.Addr().String()); err == nil {
		t.Errorf("the network was reachable: %s", out)
	}
	if out, err := run(t, s, dir, true, "net", ln.Addr().String()); err != nil {
		t.Errorf("the network was blocked although allowed: %v %s", err, out)
	}
}

func TestSandboxedCommandsGetTheirOwnTemporaryDirectory(t *testing.T) {
	s := Detect(context.Background(), exec.LookPath)
	if !s.Available() {
		t.Skip("no working sandbox on this machine")
	}
	dir := t.TempDir()
	cmd, done := s.Command(context.Background(), dir, false, "/bin/sh", "-c", `echo x > "$TMPDIR/scratch" && cat "$TMPDIR/scratch"`)
	out, err := cmd.CombinedOutput()
	done()
	if err != nil || strings.TrimSpace(string(out)) != "x" {
		t.Errorf("writing to TMPDIR: %v %s", err, out)
	}
}
