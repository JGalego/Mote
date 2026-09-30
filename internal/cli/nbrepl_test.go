package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jgalego/mote/internal/motebook"
)

// nbSessionEnv is a configured environment and the path of a notebook that
// does not exist yet.
func nbSessionEnv(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	return e, filepath.Join(t.TempDir(), "session.mote.md")
}

func TestSplitBinding(t *testing.T) {
	cases := []struct{ in, name, expr string }{
		{`chat hello`, "", `chat hello`},
		{`city = chat "capital of France"`, "city", `chat "capital of France"`},
		{`city=chat hi`, "city", `chat hi`},
		{"a = \nchat x", "a", "chat x"}, // a backslash continuation after the =
		{"a =", "", "a ="},
		{`chat "a=b"`, "", `chat "a=b"`},
		{`sh: x=1`, "", `sh: x=1`},
		{"a = chat x |\n chat y", "a", "chat x |\n chat y"},
	}
	for _, c := range cases {
		if name, expr := splitBinding(c.in); name != c.name || expr != c.expr {
			t.Errorf("splitBinding(%q) = %q, %q; want %q, %q", c.in, name, expr, c.name, c.expr)
		}
	}
}

func TestNbEditRunsCellsAndKeepsThemInTheFile(t *testing.T) {
	e, path := nbSessionEnv(t)
	input := "chat hello\ncity = chat capital\n\nchat \"again {{city}}\"\n/cells\n"
	code, out, errs := e.mote(input, "nb", "edit", path)
	if code != 0 {
		t.Fatalf("nb edit: %d %s", code, errs)
	}
	// Each output is printed as it is produced; chat echoes its prompt.
	for _, want := range []string{"echo: hello\n", "echo: capital\n", "echo: again echo: capital\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "2  city = chat capital") {
		t.Errorf("/cells:\n%s", out)
	}
	book, err := motebook.Parse(readFile(t, path))
	if err != nil || len(book.Cells) != 3 {
		t.Fatalf("notebook: %v\n%s", err, readFile(t, path))
	}
	if book.Cells[1].Name != "city" || book.Cells[0].Name != "" || book.Cells[2].Output.Text != "echo: again echo: capital" {
		t.Errorf("cells: %+v", book.Cells)
	}
	// What was typed can be run again as a notebook, and nothing has changed.
	if code, _, errs := e.mote("", "nb", "run", path); code != 0 || !strings.Contains(errs, "0 run, 3 unchanged") {
		t.Errorf("nb run after the session: %d %s", code, errs)
	}
}

func TestNbEditResumesANotebook(t *testing.T) {
	e, path := nbSessionEnv(t)
	if code, _, errs := e.mote("chat hello\n", "nb", "edit", path); code != 0 {
		t.Fatalf("first session: %d %s", code, errs)
	}
	os.WriteFile(path, []byte("# Notes\n\n```mote as=a\nchat hi\n```\n\n```output key=k\nfrom before\n```\n"), 0o644)
	code, out, errs := e.mote("chat \"got {{a}}\"\n", "nb", "edit", path)
	if code != 0 || !strings.Contains(out, "echo: got from before") {
		t.Fatalf("resume: %d %q %s", code, out, errs)
	}
	got := readFile(t, path)
	if !strings.HasPrefix(got, "# Notes\n\n```mote as=a") || !strings.Contains(got, "from before") || strings.Count(got, "```output") != 2 {
		t.Errorf("earlier content disturbed or cell not added:\n%s", got)
	}
}

func TestNbEditDoesNotKeepACellThatFailsOrIsRefused(t *testing.T) {
	e, path := nbSessionEnv(t)
	input := strings.Join([]string{
		"chat one",
		"nosuchtask x",  // unknown task
		"chat {{nope}}", // nothing binds it
		"a = chat two",
		"a = chat three",     // name already taken
		"/frobnicate",        // not a command
		"chat \"x\n```\ny\"", // cannot be told from the end of a cell
		"chat four",
	}, "\n") + "\n"
	code, out, errs := e.mote(input, "nb", "edit", path)
	if code != 0 {
		t.Fatalf("nb edit: %d %s", code, errs)
	}
	for _, want := range []string{"nosuchtask", "{{nope}} is not bound", `"a" is already bound`, "unknown command /frobnicate"} {
		if !strings.Contains(errs, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errs)
		}
	}
	book, err := motebook.Parse(readFile(t, path))
	if err != nil {
		t.Fatal(err)
	}
	var exprs []string
	for _, c := range book.Cells {
		exprs = append(exprs, c.Expr)
	}
	if strings.Join(exprs, "|") != "chat one|chat two|chat four" {
		t.Errorf("cells kept: %q", exprs)
	}
	if !strings.Contains(out, "echo: four") {
		t.Errorf("the session did not carry on after a refused cell:\n%s", out)
	}
}

func TestNbEditRefusesAValueNothingHasComputed(t *testing.T) {
	e, path := nbSessionEnv(t)
	src := "```mote as=a\nchat hi\n```\n"
	os.WriteFile(path, []byte(src), 0o644)
	code, _, errs := e.mote("chat \"{{a}}\"\n", "nb", "edit", path)
	if code != 0 || !strings.Contains(errs, "{{a}} has no output yet") || readFile(t, path) != src {
		t.Errorf("unrun name: %d %s\n%s", code, errs, readFile(t, path))
	}
}

func TestNbEditUndoDropsTheLastCell(t *testing.T) {
	e, path := nbSessionEnv(t)
	code, _, errs := e.mote("a = chat one\nchat two\n/undo\nchat \"again {{a}}\"\n/undo\n/undo\n/undo\n", "nb", "edit", path)
	if code != 0 {
		t.Fatalf("nb edit: %d %s", code, errs)
	}
	if strings.Count(errs, "dropped cell") != 3 || !strings.Contains(errs, "nothing to undo") {
		t.Errorf("undo messages:\n%s", errs)
	}
	if got := readFile(t, path); got != "" {
		t.Errorf("everything was undone, but the file holds:\n%s", got)
	}

	// A dropped cell frees its name and its value.
	code, out, errs := e.mote("a = chat one\n/undo\na = chat two\nchat \"{{a}}\"\n", "nb", "edit", path)
	if code != 0 || !strings.Contains(out, "echo: echo: two") {
		t.Errorf("name not freed: %d %q %s", code, out, errs)
	}
}

func TestNbEditUndoOnlyDropsWhatTheSessionAdded(t *testing.T) {
	e, path := nbSessionEnv(t)
	src := "```mote\nchat hi\n```\n\n```output\nold\n```\n"
	os.WriteFile(path, []byte(src), 0o644)
	code, _, errs := e.mote("/undo\n", "nb", "edit", path)
	if code != 0 || !strings.Contains(errs, "nothing to undo") || readFile(t, path) != src {
		t.Errorf("undo of an old cell: %d %s", code, errs)
	}
}

func TestNbEditContinuesALineEndingInABackslash(t *testing.T) {
	e, path := nbSessionEnv(t)
	code, out, errs := e.mote("chat \"first \\\nsecond\"\n", "nb", "edit", path)
	if code != 0 || !strings.Contains(out, "echo: first ") || !strings.Contains(out, "second") {
		t.Fatalf("continuation: %d %q %s", code, out, errs)
	}
	if book, err := motebook.Parse(readFile(t, path)); err != nil || len(book.Cells) != 1 {
		t.Errorf("notebook: %v\n%s", err, readFile(t, path))
	}
}

func TestNbEditShellCellsNeedConfirmation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stages use cmd /c on Windows; the sh syntax here does not apply")
	}
	e, path := nbSessionEnv(t)
	cell := "chat hi | sh: read l; echo \"saw $l\"\n"

	code, out, errs := e.mote(cell, "nb", "edit", path)
	if _, err := os.Stat(path); code != 0 || !strings.Contains(errs, "--yes") || out != "" || err == nil {
		t.Errorf("unconfirmed: %d %q %s (notebook created: %v)", code, out, errs, err == nil)
	}
	code, out, _ = e.mote(cell, "nb", "edit", path, "--yes")
	if code != 0 || !strings.Contains(out, "saw echo: hi") {
		t.Errorf("--yes: %d %q", code, out)
	}
	if got := readFile(t, path); !strings.Contains(got, "```output\n") {
		t.Errorf("a cell that changes things should have no key:\n%s", got)
	}

	// At a terminal it asks, and takes the answer from the same input.
	t.Setenv("MOTE_FORCE_LIVE", "1")
	os.Remove(path)
	if _, out, errs := e.mote(cell+"n\n", "nb", "edit", path); strings.Contains(out, "saw") || !strings.Contains(errs, "not run") {
		t.Errorf("declined: %q %s", out, errs)
	}
	if _, out, _ := e.mote(cell+"y\n", "nb", "edit", path); !strings.Contains(out, "saw echo: hi") {
		t.Errorf("approved: %q", out)
	}
}

func TestNbEditHelpAndUsage(t *testing.T) {
	e, path := nbSessionEnv(t)
	if code, _, errs := e.mote("/help\n", "nb", "edit", path); code != 0 || !strings.Contains(errs, "/undo") {
		t.Errorf("/help: %d %s", code, errs)
	}
	for _, args := range [][]string{
		{"nb", "edit"},
		{"nb", "edit", path, "--force"},
		{"nb", "edit", path, "-o", "x"},
		{"nb", "edit", path, "--dry-run"},
	} {
		if code, _, _ := e.mote("", args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want usage", args, code)
		}
	}
	bad := filepath.Join(t.TempDir(), "bad.md")
	os.WriteFile(bad, []byte("```mote\nchat {{x}}\n```\n"), 0o644)
	if code, _, errs := e.mote("", "nb", "edit", bad); code != ExitUsage || !strings.Contains(errs, "not bound") {
		t.Errorf("unreadable notebook: %d %s", code, errs)
	}
}
