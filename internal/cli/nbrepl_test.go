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

func TestNbConsoleRunsCellsAndKeepsThemInTheFile(t *testing.T) {
	e, path := nbSessionEnv(t)
	input := "chat hello\ncity = chat capital\n\nchat \"again {{city}}\"\n/cells\n"
	code, out, errs := e.mote(input, "nb", "console", path)
	if code != 0 {
		t.Fatalf("nb console: %d %s", code, errs)
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

func TestNbConsoleResumesANotebook(t *testing.T) {
	e, path := nbSessionEnv(t)
	if code, _, errs := e.mote("chat hello\n", "nb", "console", path); code != 0 {
		t.Fatalf("first session: %d %s", code, errs)
	}
	os.WriteFile(path, []byte("# Notes\n\n```mote as=a\nchat hi\n```\n\n```output key=k\nfrom before\n```\n"), 0o644)
	code, out, errs := e.mote("chat \"got {{a}}\"\n", "nb", "console", path)
	if code != 0 || !strings.Contains(out, "echo: got from before") {
		t.Fatalf("resume: %d %q %s", code, out, errs)
	}
	got := readFile(t, path)
	if !strings.HasPrefix(got, "# Notes\n\n```mote as=a") || !strings.Contains(got, "from before") || strings.Count(got, "```output") != 2 {
		t.Errorf("earlier content disturbed or cell not added:\n%s", got)
	}
}

func TestNbConsoleDoesNotKeepACellThatFailsOrIsRefused(t *testing.T) {
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
	code, out, errs := e.mote(input, "nb", "console", path)
	if code != 0 {
		t.Fatalf("nb console: %d %s", code, errs)
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

func TestNbConsoleRefusesAValueNothingHasComputed(t *testing.T) {
	e, path := nbSessionEnv(t)
	src := "```mote as=a\nchat hi\n```\n"
	os.WriteFile(path, []byte(src), 0o644)
	code, _, errs := e.mote("chat \"{{a}}\"\n", "nb", "console", path)
	if code != 0 || !strings.Contains(errs, "{{a}} has no output yet") || readFile(t, path) != src {
		t.Errorf("unrun name: %d %s\n%s", code, errs, readFile(t, path))
	}
}

func TestNbConsoleUndoDropsTheLastCell(t *testing.T) {
	e, path := nbSessionEnv(t)
	code, _, errs := e.mote("a = chat one\nchat two\n/undo\nchat \"again {{a}}\"\n/undo\n/undo\n/undo\n", "nb", "console", path)
	if code != 0 {
		t.Fatalf("nb console: %d %s", code, errs)
	}
	if strings.Count(errs, "dropped cell") != 3 || !strings.Contains(errs, "nothing to undo") {
		t.Errorf("undo messages:\n%s", errs)
	}
	if got := readFile(t, path); got != "" {
		t.Errorf("everything was undone, but the file holds:\n%s", got)
	}

	// A dropped cell frees its name and its value.
	code, out, errs := e.mote("a = chat one\n/undo\na = chat two\nchat \"{{a}}\"\n", "nb", "console", path)
	if code != 0 || !strings.Contains(out, "echo: echo: two") {
		t.Errorf("name not freed: %d %q %s", code, out, errs)
	}
}

func TestNbConsoleUndoOnlyDropsWhatTheSessionAdded(t *testing.T) {
	e, path := nbSessionEnv(t)
	src := "```mote\nchat hi\n```\n\n```output\nold\n```\n"
	os.WriteFile(path, []byte(src), 0o644)
	code, _, errs := e.mote("/undo\n", "nb", "console", path)
	if code != 0 || !strings.Contains(errs, "nothing to undo") || readFile(t, path) != src {
		t.Errorf("undo of an old cell: %d %s", code, errs)
	}
}

func TestNbConsoleContinuesALineEndingInABackslash(t *testing.T) {
	e, path := nbSessionEnv(t)
	code, out, errs := e.mote("chat \"first \\\nsecond\"\n", "nb", "console", path)
	if code != 0 || !strings.Contains(out, "echo: first ") || !strings.Contains(out, "second") {
		t.Fatalf("continuation: %d %q %s", code, out, errs)
	}
	if book, err := motebook.Parse(readFile(t, path)); err != nil || len(book.Cells) != 1 {
		t.Errorf("notebook: %v\n%s", err, readFile(t, path))
	}
}

func TestNbConsoleShellCellsNeedConfirmation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stages use cmd /c on Windows; the sh syntax here does not apply")
	}
	e, path := nbSessionEnv(t)
	cell := "chat hi | sh: read l; echo \"saw $l\"\n"

	code, out, errs := e.mote(cell, "nb", "console", path)
	if _, err := os.Stat(path); code != 0 || !strings.Contains(errs, "--yes") || out != "" || err == nil {
		t.Errorf("unconfirmed: %d %q %s (notebook created: %v)", code, out, errs, err == nil)
	}
	code, out, _ = e.mote(cell, "nb", "console", path, "--yes")
	if code != 0 || !strings.Contains(out, "saw echo: hi") {
		t.Errorf("--yes: %d %q", code, out)
	}
	if got := readFile(t, path); !strings.Contains(got, "```output\n") {
		t.Errorf("a cell that changes things should have no key:\n%s", got)
	}

	// At a terminal it asks, and takes the answer from the same input.
	t.Setenv("MOTE_FORCE_LIVE", "1")
	os.Remove(path)
	if _, out, errs := e.mote(cell+"n\n", "nb", "console", path); strings.Contains(out, "saw") || !strings.Contains(errs, "not run") {
		t.Errorf("declined: %q %s", out, errs)
	}
	if _, out, _ := e.mote(cell+"y\n", "nb", "console", path); !strings.Contains(out, "saw echo: hi") {
		t.Errorf("approved: %q", out)
	}
}

func TestNbConsoleHelpAndUsage(t *testing.T) {
	e, path := nbSessionEnv(t)
	if code, _, errs := e.mote("/help\n", "nb", "console", path); code != 0 || !strings.Contains(errs, "/undo") {
		t.Errorf("/help: %d %s", code, errs)
	}
	for _, args := range [][]string{
		{"nb", "console", path, "--force"},
		{"nb", "console", path, "-o", "x"},
		{"nb", "console", path, "--dry-run"},
	} {
		if code, _, _ := e.mote("", args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want usage", args, code)
		}
	}
	bad := filepath.Join(t.TempDir(), "bad.md")
	os.WriteFile(bad, []byte("```mote\nchat {{x}}\n```\n"), 0o644)
	if code, _, errs := e.mote("", "nb", "console", bad); code != ExitUsage || !strings.Contains(errs, "not bound") {
		t.Errorf("unreadable notebook: %d %s", code, errs)
	}
}

// A session with no file lives in memory: nothing is written until it is
// saved, by /save, by -o when it ends, or by saying so when asked.

func TestNbConsoleWithNoFileWritesNothingUntilSaved(t *testing.T) {
	e, _ := nbSessionEnv(t)
	dir := t.TempDir()
	chdir(t, dir)

	code, out, errs := e.mote("chat one\ncity = chat two\nchat \"{{city}}\"\n", "nb", "console")
	if code != 0 || !strings.Contains(out, "echo: echo: two") {
		t.Fatalf("session: %d %q %s", code, out, errs)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Errorf("an unsaved session left files behind: %v", left)
	}
	// Without a terminal there is nobody to ask, so it says what it dropped.
	if !strings.Contains(errs, "3 cells not saved") || !strings.Contains(errs, "-o FILE") {
		t.Errorf("stderr: %s", errs)
	}
	// An empty session has nothing to say.
	if _, _, errs := e.mote("", "nb", "console"); strings.Contains(errs, "not saved") {
		t.Errorf("an empty session complained: %s", errs)
	}
}

func TestNbConsoleSavesToTheFileNamedWithO(t *testing.T) {
	e, _ := nbSessionEnv(t)
	path := filepath.Join(t.TempDir(), "kept.mote.md")
	code, _, errs := e.mote("a = chat one\nchat \"{{a}}\"\n/undo\nchat two\n", "nb", "console", "-o", path)
	if code != 0 || !strings.Contains(errs, "saved 2 cells in "+path) {
		t.Fatalf("session: %d %s", code, errs)
	}
	book, err := motebook.Parse(readFile(t, path))
	if err != nil || len(book.Cells) != 2 || book.Cells[1].Output.Text != "echo: two" {
		t.Fatalf("kept notebook: %v\n%s", err, readFile(t, path))
	}
	// What was kept runs as a notebook.
	if code, _, errs := e.mote("", "nb", "run", path); code != 0 || !strings.Contains(errs, "0 run, 2 unchanged") {
		t.Errorf("nb run: %d %s", code, errs)
	}
	// It does not replace a file that is there, and says so before starting.
	code, out, errs := e.mote("chat three\n", "nb", "console", "-o", path)
	if code != ExitUsage || !strings.Contains(errs, "already exists") || out != "" {
		t.Errorf("existing -o: %d %q %s", code, out, errs)
	}
	if strings.Contains(readFile(t, path), "three") {
		t.Error("an existing notebook was replaced")
	}
}

func TestNbConsoleSaveCommandMakesTheSessionAFileSession(t *testing.T) {
	e, _ := nbSessionEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "s.mote.md")
	taken := filepath.Join(dir, "taken.md")
	os.WriteFile(taken, []byte("mine\n"), 0o644)

	input := strings.Join([]string{
		"/save", // no name
		"chat one",
		"/save " + taken, // exists
		"/save " + path,
		"chat two",       // from here on it is saved as it goes
		"/save again.md", // already saved
	}, "\n") + "\n"
	code, _, errs := e.mote(input, "nb", "console")
	if code != 0 {
		t.Fatalf("session: %d %s", code, errs)
	}
	for _, want := range []string{"usage: /save FILE", taken + " already exists", "saved 1 cell in " + path, "already saved in " + path} {
		if !strings.Contains(errs, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errs)
		}
	}
	if readFile(t, taken) != "mine\n" {
		t.Error("/save replaced a file")
	}
	book, err := motebook.Parse(readFile(t, path))
	if err != nil || len(book.Cells) != 2 {
		t.Errorf("cells were not saved as they ran: %v\n%s", err, readFile(t, path))
	}
	if strings.Contains(errs, "not saved") {
		t.Errorf("a saved session said it was dropping cells:\n%s", errs)
	}
}

func TestNbConsoleAsksWhetherToSaveWhenItEndsAtATerminal(t *testing.T) {
	t.Setenv("MOTE_FORCE_LIVE", "1")
	e, _ := nbSessionEnv(t)
	dir := t.TempDir()
	chdir(t, dir)
	named := filepath.Join(dir, "named.mote.md")

	// No: nothing is written.
	code, _, errs := e.mote("chat one\n/exit\nn\n", "nb", "console")
	if code != 0 || !strings.Contains(errs, "save the 1 cell you ran to a file? [y/N]") {
		t.Fatalf("declined: %d %s", code, errs)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Errorf("declining still wrote %v", left)
	}

	// Yes and a name.
	if code, _, errs := e.mote("chat one\n/exit\ny\n"+named+"\n", "nb", "console"); code != 0 || !strings.Contains(readFile(t, named), "echo: one") {
		t.Errorf("named: %d %s", code, errs)
	}

	// Yes and Enter takes the default name.
	if code, _, errs := e.mote("chat two\n/exit\ny\n\n", "nb", "console"); code != 0 || !strings.Contains(readFile(t, filepath.Join(dir, defaultSessionName)), "echo: two") {
		t.Errorf("default name: %d %s", code, errs)
	}

	// A name that is taken is refused and asked for again, since the cells
	// exist only in memory; and n gives them up.
	code, _, errs = e.mote("chat three\n/exit\ny\n"+named+"\nn\n", "nb", "console")
	if code != 0 || !strings.Contains(errs, named+" already exists") || strings.Contains(readFile(t, named), "three") {
		t.Errorf("taken name: %d %s", code, errs)
	}
	other := filepath.Join(dir, "other.mote.md")
	if code, _, errs := e.mote("chat three\n/exit\ny\n"+named+"\n"+other+"\n", "nb", "console"); code != 0 || !strings.Contains(readFile(t, other), "echo: three") {
		t.Errorf("second try: %d %s", code, errs)
	}
	// A name that cannot be written is asked for again too.
	bad := filepath.Join(dir, "no", "such", "dir", "x.md")
	good := filepath.Join(dir, "good.mote.md")
	if code, _, errs := e.mote("chat four\n/exit\ny\n"+bad+"\n"+good+"\n", "nb", "console"); code != 0 || !strings.Contains(readFile(t, good), "echo: four") {
		t.Errorf("unwritable name: %d %s", code, errs)
	}

	// Ending with Ctrl-D asks the same, and running out of input answers no.
	code, _, errs = e.mote("chat five\n", "nb", "console")
	if code != 0 || !strings.Contains(errs, "save the 1 cell") {
		t.Errorf("end of input: %d %s", code, errs)
	}
	// A session on a file has nothing to ask.
	path := filepath.Join(dir, "file.mote.md")
	if _, _, errs := e.mote("chat six\n/exit\n", "nb", "console", path); strings.Contains(errs, "save the") {
		t.Errorf("a file session asked to save: %s", errs)
	}
}
