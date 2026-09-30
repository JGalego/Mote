package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func stateOf(t *testing.T, path string) map[string]string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal([]byte(readFile(t, path)), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestNbExecRunsTheCellOnStdin(t *testing.T) {
	e, _ := nbSessionEnv(t)
	code, out, errs := e.mote("chat hello\n", "nb", "exec")
	if code != 0 || out != "echo: hello\n" {
		t.Fatalf("exec: %d %q %s", code, out, errs)
	}
	// A pipeline is one cell.
	if code, out, _ := e.mote(`chat one | chat "then {}"`, "nb", "exec"); code != 0 || out != "echo: then echo: one\n" {
		t.Errorf("pipeline: %d %q", code, out)
	}
}

func TestNbExecKeepsWhatCellsBindInTheStateFile(t *testing.T) {
	e, _ := nbSessionEnv(t)
	state := filepath.Join(t.TempDir(), "state.json")

	if code, out, errs := e.mote("city = chat capital\n", "nb", "exec", "--state", state); code != 0 || out != "echo: capital\n" {
		t.Fatalf("bind: %d %q %s", code, out, errs)
	}
	if got := stateOf(t, state); got["city"] != "echo: capital" || len(got) != 1 {
		t.Errorf("state %v", got)
	}
	// Another process reads it back.
	code, out, errs := e.mote(`chat "got {{city}}"`, "nb", "exec", "--state", state)
	if code != 0 || out != "echo: got echo: capital\n" {
		t.Fatalf("read: %d %q %s", code, out, errs)
	}
	// Running a cell again is what a notebook is for: the name is rebound.
	if code, _, errs := e.mote("city = chat other\n", "nb", "exec", "--state", state); code != 0 {
		t.Fatalf("rebind: %d %s", code, errs)
	}
	if got := stateOf(t, state); got["city"] != "echo: other" {
		t.Errorf("state after rebinding %v", got)
	}
	// A cell that does not bind leaves the state alone.
	before := readFile(t, state)
	e.mote("chat plain\n", "nb", "exec", "--state", state)
	if readFile(t, state) != before {
		t.Error("a cell that binds nothing rewrote the state")
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(state); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("state file mode %v (%v); it holds model output", fi.Mode(), err)
		}
	}
}

func TestNbExecRefusesWhatCannotRun(t *testing.T) {
	e, _ := nbSessionEnv(t)
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	garbled := filepath.Join(dir, "garbled.json")
	os.WriteFile(garbled, []byte("{nope"), 0o644)
	null := filepath.Join(dir, "null.json")
	os.WriteFile(null, []byte("null"), 0o644)

	cases := []struct {
		name  string
		stdin string
		args  []string
		want  string
	}{
		{"nothing to run", "  \n", []string{"nb", "exec"}, "no cell"},
		{"binding without a state file", "a = chat x", []string{"nb", "exec"}, "needs --state"},
		{"unknown task", "nosuchtask x", []string{"nb", "exec"}, "nosuchtask"},
		{"a value never bound", "chat {{x}}", []string{"nb", "exec", "--state", state}, "{{x}} has no value"},
		{"state that is not JSON", "chat x", []string{"nb", "exec", "--state", garbled}, "not a session's state"},
		{"a shell cell nobody approved", "chat x | sh: read l", []string{"nb", "exec"}, "--yes"},
		{"a flag that is not exec's", "chat x", []string{"nb", "exec", "--force"}, "does not apply"},
		{"a file", "chat x", []string{"nb", "exec", "book.md"}, "usage"},
	}
	for _, c := range cases {
		code, out, errs := e.mote(c.stdin, c.args...)
		if code != ExitUsage && c.name != "state that is not JSON" {
			t.Errorf("%s: exit %d, want usage", c.name, code)
		}
		if code == 0 || out != "" || !strings.Contains(errs, c.want) {
			t.Errorf("%s: %d %q %s, want %q", c.name, code, out, errs, c.want)
		}
	}
	// A state file that holds null is an empty session, not a crash.
	if code, _, errs := e.mote("a = chat x", "nb", "exec", "--state", null); code != 0 {
		t.Errorf("null state: %d %s", code, errs)
	}
	if _, err := os.Stat(state); err == nil {
		t.Error("a refused cell created the state file")
	}
}

func TestNbExecRunsAnApprovedShellCell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stages use cmd /c on Windows; the sh syntax here does not apply")
	}
	e, _ := nbSessionEnv(t)
	code, out, errs := e.mote("chat hi | sh: read l; echo \"saw $l\"", "nb", "exec", "--yes")
	if code != 0 || out != "saw echo: hi\n" {
		t.Errorf("--yes: %d %q %s", code, out, errs)
	}
}

func TestNbRejectsFlagsForTheWrongSubcommand(t *testing.T) {
	e, path := nbSessionEnv(t)
	for _, args := range [][]string{
		{"nb", "run", path, "--state", "s.json"},
		{"nb", "edit", path, "--state", "s.json"},
		{"nb", "frobnicate"},
		{"nb"},
	} {
		if code, _, _ := e.mote("", args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want usage", args, code)
		}
	}
}
