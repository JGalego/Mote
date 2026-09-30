package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// nbEnv is a configured environment with a notebook written to disk.
func nbEnv(t *testing.T, src string) (*env, string) {
	t.Helper()
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	path := filepath.Join(t.TempDir(), "book.mote.md")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return e, path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const twoCells = "# Notes\n\n" +
	"```mote as=a\nchat hello\n```\n\n" +
	"Then:\n\n" +
	"```mote\nchat \"again {{a}}\"\n```\n"

func TestNbRunWritesOutputsUnderTheirCells(t *testing.T) {
	e, path := nbEnv(t, twoCells)
	code, _, errs := e.mote("", "nb", "run", path)
	if code != 0 {
		t.Fatalf("nb run: %d %s", code, errs)
	}
	got := readFile(t, path)
	// chat echoes its prompt, so the second cell shows it read {{a}}.
	for _, want := range []string{"echo: hello\n```", "echo: again echo: hello\n```", "# Notes", "Then:"} {
		if !strings.Contains(got, want) {
			t.Errorf("notebook lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "```output key=") != 2 {
		t.Errorf("want two keyed outputs:\n%s", got)
	}
	if !strings.Contains(errs, "2 run, 0 unchanged") {
		t.Errorf("summary: %s", errs)
	}
}

func TestNbRunSkipsCellsWhoseInputsAreUnchanged(t *testing.T) {
	e, path := nbEnv(t, twoCells)
	if code, _, errs := e.mote("", "nb", "run", path); code != 0 {
		t.Fatalf("first run: %d %s", code, errs)
	}
	first := readFile(t, path)

	code, _, errs := e.mote("", "nb", "run", path)
	if code != 0 || !strings.Contains(errs, "0 run, 2 unchanged") {
		t.Fatalf("second run: %d %s", code, errs)
	}
	if readFile(t, path) != first {
		t.Error("an unchanged run rewrote the notebook")
	}

	// Editing the first cell reruns it and, because it reads its output,
	// the second one, although the second cell's own text is the same.
	edited := strings.Replace(first, "chat hello", "chat goodbye", 1)
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs = e.mote("", "nb", "run", path)
	if code != 0 || !strings.Contains(errs, "2 run, 0 unchanged") {
		t.Fatalf("after edit: %d %s", code, errs)
	}
	if got := readFile(t, path); !strings.Contains(got, "echo: again echo: goodbye") {
		t.Errorf("downstream cell not recomputed:\n%s", got)
	}

	if code, _, errs := e.mote("", "nb", "run", path, "--force"); code != 0 || !strings.Contains(errs, "2 run, 0 unchanged") {
		t.Errorf("--force: %d %s", code, errs)
	}
}

func TestNbRunLeavesValuesLiteral(t *testing.T) {
	// A value with a | or a {} in it must reach the next cell as text, not
	// become a pipeline stage or be taken for the piped value.
	src := "```mote as=a\nchat 'x | y {}'\n```\n\n```mote\nchat \"got {{a}}\"\n```\n"
	e, path := nbEnv(t, src)
	if code, _, errs := e.mote("", "nb", "run", path); code != 0 {
		t.Fatalf("nb run: %d %s", code, errs)
	}
	if got := readFile(t, path); !strings.Contains(got, "echo: got echo: x | y {}") {
		t.Errorf("value changed on the way:\n%s", got)
	}
}

func TestNbRunDryRunChangesNothing(t *testing.T) {
	e, path := nbEnv(t, twoCells)
	code, _, errs := e.mote("", "nb", "run", path, "--dry-run")
	if code != 0 || strings.Count(errs, "would run") != 2 {
		t.Fatalf("dry run: %d %s", code, errs)
	}
	if readFile(t, path) != twoCells {
		t.Error("--dry-run modified the notebook")
	}
	if code, _, _ := e.mote("", "nb", "run", path); code != 0 {
		t.Fatal("run failed")
	}
	// Change only the first cell: the second would run too, being downstream.
	edited := strings.Replace(readFile(t, path), "chat hello", "chat other", 1)
	os.WriteFile(path, []byte(edited), 0o644)
	if _, _, errs := e.mote("", "nb", "run", path, "--dry-run"); strings.Count(errs, "would run") != 2 {
		t.Errorf("downstream cell not predicted to run: %s", errs)
	}
}

func TestNbRunOutputElsewhere(t *testing.T) {
	e, path := nbEnv(t, twoCells)
	code, out, errs := e.mote("", "nb", "run", path, "-o", "-")
	if code != 0 || !strings.Contains(out, "echo: again echo: hello") {
		t.Fatalf("-o -: %d %q %s", code, out, errs)
	}
	if readFile(t, path) != twoCells {
		t.Error("-o - modified the notebook")
	}
	dest := filepath.Join(t.TempDir(), "copy.md")
	if code, _, errs := e.mote("", "nb", "run", path, "-o", dest); code != 0 {
		t.Fatalf("-o FILE: %d %s", code, errs)
	}
	if !strings.Contains(readFile(t, dest), "echo: hello") || readFile(t, path) != twoCells {
		t.Error("-o FILE did not leave the source alone")
	}
}

func TestNbRunReportsProblemsBeforeRunning(t *testing.T) {
	e, path := nbEnv(t, "```mote\nchat hello\n```\n\n```mote\nnosuchtask x\n```\n")
	code, _, errs := e.mote("", "nb", "run", path)
	if code != ExitUsage || !strings.Contains(errs, "cell 2 (line 5)") || !strings.Contains(errs, "nosuchtask") {
		t.Errorf("unknown task: %d %s", code, errs)
	}
	if strings.Contains(readFile(t, path), "output") {
		t.Error("a cell ran although a later one could not")
	}

	cases := map[string]string{
		"```mote\nchat {{nope}}\n```\n": "not bound",
		"just prose\n":                  "no cells",
		"```mote\nchat a | \n```\n":     "empty stage",
		"```mote as=a\nchat hi\n```\n```mote\nsh: echo {{a}}\n```\n": "cannot go into a shell command",
	}
	for src, want := range cases {
		os.WriteFile(path, []byte(src), 0o644)
		if code, _, errs := e.mote("", "nb", "run", path); code != ExitUsage || !strings.Contains(errs, want) {
			t.Errorf("%q: %d %s, want %q", src, code, errs, want)
		}
	}
	if code, _, _ := e.mote("", "nb", "run", filepath.Join(t.TempDir(), "missing.md")); code == 0 {
		t.Error("a missing file was accepted")
	}
	for _, args := range [][]string{{"nb"}, {"nb", "run"}, {"nb", "walk", path}, {"nb", "run", path, "extra"}} {
		if code, _, _ := e.mote("", args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want usage", args, code)
		}
	}
}

func TestNbRunShellCellsNeedConfirmationAndAlwaysRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stages use cmd /c on Windows; the sh syntax here does not apply")
	}
	src := "```mote\nchat hello | sh: read l; echo \"saw [$l]\"\n```\n"
	e, path := nbEnv(t, src)

	code, _, errs := e.mote("", "nb", "run", path)
	if code != ExitUsage || !strings.Contains(errs, "--yes") {
		t.Errorf("unconfirmed shell cell: %d %s", code, errs)
	}
	if readFile(t, path) != src {
		t.Error("the cell ran without confirmation")
	}

	if code, _, errs := e.mote("", "nb", "run", path, "--yes"); code != 0 {
		t.Fatalf("--yes: %d %s", code, errs)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "saw [echo: hello]") || !strings.Contains(got, "```output\n") {
		t.Errorf("shell cell output:\n%s", got)
	}
	// Running it again is its point, so it has no key to match.
	if _, _, errs := e.mote("", "nb", "run", path, "--yes"); !strings.Contains(errs, "1 run, 0 unchanged") {
		t.Errorf("shell cell was skipped: %s", errs)
	}
}

func TestNbRunKeepsEarlierOutputsWhenACellFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stages use cmd /c on Windows; the sh syntax here does not apply")
	}
	e, path := nbEnv(t, "```mote\nchat hello\n```\n\n```mote\nchat hi | sh: exit 3\n```\n")
	code, _, errs := e.mote("", "nb", "run", path, "--yes")
	if code == 0 || !strings.Contains(errs, "cell 2 (line 5)") {
		t.Fatalf("failing cell: %d %s", code, errs)
	}
	if got := readFile(t, path); !strings.Contains(got, "echo: hello") {
		t.Errorf("progress lost:\n%s", got)
	}
}

func TestWriteFileAtomicKeepsTheMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no permission bits to keep")
	}
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 || readFile(t, p) != "new" {
		t.Errorf("mode %v, err %v, content %q", fi.Mode(), err, readFile(t, p))
	}
	if left, _ := filepath.Glob(p + ".tmp*"); len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
	if err := writeFileAtomic(filepath.Join(t.TempDir(), "no", "such", "dir", "f"), nil, 0o644); err == nil {
		t.Error("writing into a missing directory succeeded")
	}
}

func TestNbRunAsksBeforeCellsThatCanChangeThings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stages use cmd /c on Windows; the sh syntax here does not apply")
	}
	t.Setenv("MOTE_FORCE_LIVE", "1")
	src := "```mote\nchat hello | sh: read l; echo \"$l\"\n```\n"
	e, path := nbEnv(t, src)

	code, _, errs := e.mote("n\n", "nb", "run", path)
	if code == 0 || !strings.Contains(errs, "cell 1 (line 1)") || readFile(t, path) != src {
		t.Errorf("declined: %d %s", code, errs)
	}
	code, _, errs = e.mote("y\n", "nb", "run", path)
	if code != 0 || !strings.Contains(readFile(t, path), "echo: hello") {
		t.Errorf("approved: %d %s", code, errs)
	}
}

func TestNbRunRejectsUnknownFlags(t *testing.T) {
	e, path := nbEnv(t, twoCells)
	if code, _, _ := e.mote("", "nb", "run", path, "--bogus"); code != ExitUsage {
		t.Errorf("unknown flag: exit %d", code)
	}
}
