package cli

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// completion runs Tab at the end of line, or at the cursor marked by ^.
func completion(t *testing.T, s *nbSession, line string) (int, string) {
	t.Helper()
	pos := len([]rune(line))
	if i := strings.Index(line, "^"); i >= 0 {
		line = strings.Replace(line, "^", "", 1)
		pos = len([]rune(line[:i]))
	}
	start, cands := s.complete([]rune(line), pos)
	return start, strings.Join(cands, "|")
}

func completionSession(t *testing.T) *nbSession {
	t.Helper()
	return &nbSession{
		tasks:  builtins(t),
		values: map[string]string{"city": "Paris", "capital": "Paris", "zebra": "z"},
	}
}

func TestConsoleCompletesCommands(t *testing.T) {
	s := completionSession(t)
	for line, want := range map[string]string{
		"/ta":    "/tasks",
		"/s":     "/save ",
		"/e":     "/examples|/exit",
		"/zzz":   "",
		"/save ": "",
	} {
		if _, got := completion(t, s, line); got != want {
			t.Errorf("%q: %q, want %q", line, got, want)
		}
	}
	if _, got := completion(t, s, "/"); !strings.Contains(got, "/tasks") || !strings.Contains(got, "/examples") || !strings.Contains(got, "/vars") {
		t.Errorf("a bare /: %q", got)
	}
}

func TestConsoleCompletesTasksWhereATaskGoes(t *testing.T) {
	s := completionSession(t)
	for _, c := range []struct {
		line  string
		start int
		want  string
	}{
		{"ch", 0, "chat "},
		{"co", 0, "code |commit "},
		{"sh", 0, "sh: "},
		{"city = tr", 7, "transcribe |translate "},
		{"city=tr", 5, "transcribe |translate "},
		{"chat hi | des", 10, "describe "},
		{"chat hi|des", 8, "describe "},
		{"  ch", 2, "chat "},
		{"nosuch", 0, ""},
	} {
		if start, got := completion(t, s, c.line); got != c.want || (got != "" && start != c.start) {
			t.Errorf("%q: %d %q, want %d %q", c.line, start, got, c.start, c.want)
		}
	}
	// An empty prompt offers every task a cell can run, and none that needs -o.
	_, all := completion(t, s, "")
	if !strings.Contains(all, "chat ") || !strings.Contains(all, "sh: ") || strings.Contains(all, "convert") {
		t.Errorf("an empty prompt offers %q", all)
	}
	// Not at a task's place, a word is not completed as one.
	if _, got := completion(t, s, "chat ch"); strings.Contains(got, "chat ") {
		t.Errorf("an argument completed as a task: %q", got)
	}
}

func TestConsoleCompletesNamesInBraces(t *testing.T) {
	s := completionSession(t)
	for _, c := range []struct {
		line  string
		start int
		want  string
	}{
		{`chat "{{`, 8, "capital}}|city}}|zebra}}"},
		{`chat "{{ci`, 8, "city}}"},
		{`chat "how tall is {{ci`, 20, "city}}"}, // 6 + "how tall is " + {{
		{`chat "{{city}} and {{ca`, 21, "capital}}"},
		{`chat "{{city}} and {{ci^ty}}`, 21, "city}}"}, // only what is before the cursor counts
		{`chat "{{nope`, 8, ""},
	} {
		start, got := completion(t, s, c.line)
		if c.want == "" && got == "" {
			continue
		}
		if got != c.want || start != c.start {
			t.Errorf("%q: %d %q, want %d %q", c.line, start, got, c.start, c.want)
		}
	}
	// The start is a rune index, not a byte one, after non-ASCII text.
	line := `chat "café {{ci`
	if start, got := completion(t, s, line); got != "city}}" || start != len([]rune(`chat "café {{`)) {
		t.Errorf("after non-ASCII text: %d %q", start, got)
	}
}

func TestConsoleCompletesFileNames(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	os.MkdirAll(filepath.Join(dir, "examples", "sub"), 0o755)
	for _, f := range []string{"examples/meeting.m4a", "examples/clip.mp4", "examples/my notes.txt", "examples/.hidden", "notes.txt"} {
		os.WriteFile(filepath.Join(dir, f), nil, 0o644)
	}
	s := completionSession(t)

	for _, c := range []struct {
		line  string
		start int
		want  string
	}{
		{"transcribe exam", 11, "examples/"},
		{"transcribe examples/", 11, "examples/clip.mp4|examples/meeting.m4a|examples/sub/"},
		{"transcribe examples/me", 11, "examples/meeting.m4a"},
		{"transcribe examples/.", 11, "examples/.hidden"},
		{"transcribe n", 11, "notes.txt"},
		{"transcribe ", 11, ""},
		{"transcribe nothing", 11, ""},
		{"transcribe missing/dir/x", 11, ""},
		// A name with a space is offered only where it can be written as it is.
		{`transcribe "examples/my`, 12, "examples/my notes.txt"},
		{"transcribe examples/my", 11, ""},
		{"chat hi | describe examples/cl", 19, "examples/clip.mp4"},
	} {
		start, got := completion(t, s, c.line)
		if got != c.want || (got != "" && start != c.start) {
			t.Errorf("%q: %d %q, want %d %q", c.line, start, got, c.start, c.want)
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(dir, "examples"), filepath.Join(dir, "link")); err == nil {
			if _, got := completion(t, s, "transcribe li"); got != "link/" {
				t.Errorf("a link to a directory: %q", got)
			}
		}
	}
}

func TestEditorInputCompletesAndRecalls(t *testing.T) {
	a, out := testApp()
	s := completionSession(t)
	in := &editorInput{a: a, r: bufio.NewReader(strings.NewReader(
		"/ta\t\r" + // Tab finishes a command
			"ci\t = ch\t\"hi\"\r" + // the first word is not a task: nothing is inserted
			"\x1b[A\r" + // Up recalls the last turn
			"ch\t\r")), complete: s.complete}
	for i, want := range []string{"/tasks", `ci = chat "hi"`, `ci = chat "hi"`, "chat"} {
		got, ok := in.turn("In [1]: ")
		if !ok || strings.TrimSpace(got) != want {
			t.Errorf("turn %d: %q %v, want %q", i+1, got, ok, want)
		}
	}
	// At the end of the input the cursor is moved off the prompt's line.
	out.Reset()
	if _, ok := in.turn("In [1]: "); ok || !strings.HasSuffix(out.String(), "\n") {
		t.Errorf("Ctrl-D: ok %v, drew %q", ok, out.String())
	}
}

func TestEditorInputAnswersQuestions(t *testing.T) {
	a, out := testApp()
	in := &editorInput{a: a, r: bufio.NewReader(strings.NewReader("ye" + "\b" + "y\r"))}
	if line, ok := in.line("save? "); !ok || line != "yy" || !strings.Contains(out.String(), "save? ") {
		t.Errorf("line: %q %v, drew %q", line, ok, out.String())
	}
	if _, ok := in.line("again? "); ok {
		t.Error("the end of input answered a question")
	}
	if in.err() != nil {
		t.Errorf("err: %v", in.err())
	}
	in.resume() // nothing to do, and no harm
}

func TestPlainInputAsksAfterTheEndOfInput(t *testing.T) {
	a, _ := testApp()
	a.in = strings.NewReader("chat one\n")
	in := newPlainInput(a, false)
	if line, ok := in.turn("> "); !ok || line != "chat one" {
		t.Fatalf("turn: %q %v", line, ok)
	}
	if _, ok := in.turn("> "); ok {
		t.Fatal("no more input, and a turn came back")
	}
	if in.err() != nil {
		t.Errorf("err: %v", in.err())
	}
	// A scanner that has reported the end reads nothing more; after resume it
	// asks the reader again, which here has nothing either.
	in.resume()
	if _, ok := in.line("save? "); ok {
		t.Error("an answer came from nowhere")
	}
}

func TestConsoleListsWhatIsAvailable(t *testing.T) {
	e, path := nbSessionEnv(t)
	code, out, errs := e.mote("/tasks\n/vars\nresult = chat one\n/vars\n/examples\n/help\n", "nb", "console", path)
	if code != 0 {
		t.Fatalf("console: %d %s", code, errs)
	}
	// /tasks: each task with the arguments it takes and what it does, and not
	// the ones a cell cannot run.
	if !strings.Contains(out, "chat PROMPT") || !strings.Contains(out, "transcribe AUDIO") || strings.Contains(out, "convert ") {
		t.Errorf("/tasks:\n%s", out)
	}
	// /vars: nothing at first, then what was bound.
	if !strings.Contains(errs, "nothing is bound yet") || !strings.Contains(out, "result = echo: one") {
		t.Errorf("/vars:\nout: %s\nerr: %s", out, errs)
	}
	// /examples: every one, ready to paste.
	for _, ex := range consoleExamples {
		if !strings.Contains(out, ex+"\n") {
			t.Errorf("/examples lacks %q", ex)
		}
	}
	// /help points at all of it.
	for _, want := range []string{"/tasks", "/examples", "/vars", "/cells", "/undo", "/save", "Tab completes", "arrow keys"} {
		if !strings.Contains(errs, want) {
			t.Errorf("/help lacks %q:\n%s", want, errs)
		}
	}
}

func TestConsoleHelpsWhenYouTypeASentence(t *testing.T) {
	e, path := nbSessionEnv(t)
	code, out, errs := e.mote("hey there\nchat ok | nosuchstage\nchat \"a\" | sh: cat\n", "nb", "console", path)
	if code != 0 || out != "" {
		t.Fatalf("console: %d %q %s", code, out, errs)
	}
	// The way out, in the user's own words, and no line number for a line
	// that is not in any file.
	if !strings.Contains(errs, `unknown task "hey"; a cell is a task, like chat "hey there"`) ||
		!strings.Contains(errs, "/tasks lists them") || !strings.Contains(errs, "/examples") {
		t.Errorf("a sentence:\n%s", errs)
	}
	if strings.Contains(errs, "(line ") {
		t.Errorf("a console error names a line:\n%s", errs)
	}
	if !strings.Contains(errs, `unknown task "nosuchstage" in stage 2`) {
		t.Errorf("a later stage:\n%s", errs)
	}
	if _, err := os.Stat(path); err == nil {
		t.Errorf("a refused cell created the notebook:\n%s", readFile(t, path))
	}
}

func TestCellTasksLeaveOutWhatNeedsAnOutputPath(t *testing.T) {
	ids := map[string]bool{}
	for _, tk := range cellTasks(builtins(t)) {
		ids[tk.ID] = true
	}
	if !ids["chat"] || !ids["draw"] || ids["convert"] {
		t.Errorf("cell tasks: %v", ids)
	}
}
