package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jgalego/mote/internal/runtime"
)

// The test binary doubles as the program exec steps run, so these tests
// behave the same on every platform. MOTE_EXEC_HELPER picks what it does.
func TestMain(m *testing.M) {
	switch os.Getenv("MOTE_EXEC_HELPER") {
	case "":
		os.Exit(m.Run())
	case "args":
		// Report the arguments and working directory exactly as received.
		wd, _ := os.Getwd()
		stdin, _ := io.ReadAll(os.Stdin)
		json.NewEncoder(os.Stdout).Encode(map[string]any{"args": os.Args[1:], "wd": wd, "stdin": string(stdin)})
	case "fail":
		fmt.Fprintln(os.Stdout, "partial output")
		fmt.Fprintln(os.Stderr, "helper: no such pattern")
		os.Exit(3)
	case "flood":
		chunk := strings.Repeat("x", 4096)
		for i := 0; i < (maxRead/4096)+10; i++ {
			os.Stdout.WriteString(chunk)
		}
	case "sleep":
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

// helperEnv resolves the program "helper" to the test binary, running in
// the given mode; any other program is missing.
func helperEnv(t *testing.T, mode string) Env {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOTE_EXEC_HELPER", mode)
	e := env(&fakeBackend{reply: func(r runtime.Request) string { return "model: " + r.Prompt }})
	e.Tool = func(name string) (string, error) {
		if name == "helper" {
			return self, nil
		}
		return "", errors.New(name + " is not installed")
	}
	return e
}

func execTask(t *testing.T, doc string) Task {
	t.Helper()
	tasks, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return tasks[0]
}

type helperReport struct {
	Args  []string `json:"args"`
	WD    string   `json:"wd"`
	Stdin string   `json:"stdin"`
}

func report(t *testing.T, text string) helperReport {
	t.Helper()
	var r helperReport
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		t.Fatalf("helper output %q: %v", text, err)
	}
	return r
}

const grepTask = `{"tasks":[{"id":"grep","summary":"Search","in":["text"],"out":"text",
  "params":[{"name":"pattern","kind":"text"},{"name":"dir","kind":"dir","optional":true}],
  "steps":[{"op":"exec","cmd":["helper","-n","--","{{pattern}}","{{dir}}"],"as":"out"}]}]}`

func TestExecPassesEachValueAsOneArgument(t *testing.T) {
	tk := execTask(t, grepTask)
	dir := t.TempDir()
	// Spaces, quotes and shell syntax arrive untouched, as one argument.
	nasty := `a "b" c; rm -rf / $(whoami) | tee x`
	res, err := tk.Run(context.Background(), helperEnv(t, "args"), []string{nasty, dir}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := report(t, res.Text).Args
	want := []string{"-n", "--", nasty, dir}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("args %q, want %q", got, want)
	}
	if tk.Tools()[0] != "helper" {
		t.Errorf("tools %v should name the program", tk.Tools())
	}
}

func TestExecDropsAnUnsetOptionalValue(t *testing.T) {
	tk := execTask(t, grepTask)
	res, err := tk.Run(context.Background(), helperEnv(t, "args"), []string{"needle"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := report(t, res.Text).Args; strings.Join(got, " ") != "-n -- needle" {
		t.Errorf("args %q: an unset optional should leave no empty argument", got)
	}
}

func TestExecExpandsValuesInsideAnArgument(t *testing.T) {
	tk := execTask(t, `{"tasks":[{"id":"x","params":[{"name":"n","kind":"text"}],
	  "steps":[{"op":"exec","cmd":["helper","--max-count={{n}}"],"as":"out"}]}]}`)
	res, err := tk.Run(context.Background(), helperEnv(t, "args"), []string{"3"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := report(t, res.Text).Args; len(got) != 1 || got[0] != "--max-count=3" {
		t.Errorf("args %q", got)
	}
}

func TestExecRefusesValuesThatLookLikeOptions(t *testing.T) {
	// Without a -- in cmd, a value starting with - would reach the program
	// as an option: for rg, --pre=CMD runs CMD on every file.
	tk := execTask(t, `{"tasks":[{"id":"x","params":[{"name":"pattern","kind":"text"}],
	  "steps":[{"op":"exec","cmd":["helper","{{pattern}}"],"as":"out"}]}]}`)
	_, err := tk.Run(context.Background(), helperEnv(t, "args"), []string{"--pre=sh"}, Options{})
	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), `put "--" before {{pattern}}`) {
		t.Fatalf("option-like value accepted or unexplained: %v", err)
	}
	// After a --, the same value is an operand and is passed on.
	res, err := execTask(t, grepTask).Run(context.Background(), helperEnv(t, "args"), []string{"--pre=sh"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := report(t, res.Text).Args; got[len(got)-1] != "--pre=sh" {
		t.Errorf("args %q", got)
	}
}

func TestExecStdinAndWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	tk := execTask(t, `{"tasks":[{"id":"x","params":[{"name":"text","kind":"text"},{"name":"where","kind":"dir"}],
	  "steps":[{"op":"exec","cmd":["helper"],"from":"text","dir":"where","as":"out"}]}]}`)
	res, err := tk.Run(context.Background(), helperEnv(t, "args"), []string{"line one\nline two", dir}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	r := report(t, res.Text)
	if r.Stdin != "line one\nline two" {
		t.Errorf("stdin %q", r.Stdin)
	}
	want, _ := filepath.EvalSymlinks(dir)
	if got, _ := filepath.EvalSymlinks(r.WD); got != want {
		t.Errorf("working directory %q, want %q", got, want)
	}
}

func TestExecFeedsGeneratedTextToTheProgram(t *testing.T) {
	// exec composes with the other ops: here a model step's reply is the
	// program's stdin, as in "write some JSON, then check it".
	tk := execTask(t, `{"tasks":[{"id":"x","params":[{"name":"prompt","kind":"text"}],
	  "steps":[{"op":"generate","cap":"text","prompt":"{{prompt}}","as":"draft"},
	           {"op":"exec","cmd":["helper"],"from":"draft","as":"out"}]}]}`)
	res, err := tk.Run(context.Background(), helperEnv(t, "args"), []string{"hi"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := report(t, res.Text).Stdin; got != "model: hi" {
		t.Errorf("stdin %q", got)
	}
}

func TestExecFailuresSayWhy(t *testing.T) {
	tk := execTask(t, `{"tasks":[{"id":"x","params":[],"steps":[{"op":"exec","cmd":["helper"],"as":"out"}]}]}`)
	_, err := tk.Run(context.Background(), helperEnv(t, "fail"), nil, Options{})
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "no such pattern") {
		t.Errorf("failure should carry the exit status and stderr: %v", err)
	}

	missing := execTask(t, `{"tasks":[{"id":"x","params":[],"steps":[{"op":"exec","cmd":["nonesuch"],"as":"out"}]}]}`)
	if _, err := missing.Run(context.Background(), helperEnv(t, "args"), nil, Options{}); err == nil || !strings.Contains(err.Error(), "nonesuch") {
		t.Errorf("missing program: %v", err)
	}
}

func TestExecCapsOutput(t *testing.T) {
	var log strings.Builder
	e := helperEnv(t, "flood")
	e.Log = &log
	tk := execTask(t, `{"tasks":[{"id":"x","params":[],"steps":[{"op":"exec","cmd":["helper"],"as":"out"}]}]}`)
	res, err := tk.Run(context.Background(), e, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Text) != maxRead {
		t.Errorf("kept %d bytes, want %d", len(res.Text), maxRead)
	}
	if !strings.Contains(log.String(), "dropped") {
		t.Errorf("truncation not reported: %q", log.String())
	}
}

func TestExecStopsWhenCancelled(t *testing.T) {
	tk := execTask(t, `{"tasks":[{"id":"x","params":[],"steps":[{"op":"exec","cmd":["helper"],"as":"out"}]}]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := tk.Run(ctx, helperEnv(t, "sleep"), nil, Options{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err %v", err)
	}
	if time.Since(start) > 20*time.Second {
		t.Error("the program outlived its context")
	}
}

func TestParseRejectsBadExecSteps(t *testing.T) {
	bad := map[string]string{
		"no cmd":         `{"tasks":[{"id":"x","steps":[{"op":"exec","as":"out"}]}]}`,
		"blank program":  `{"tasks":[{"id":"x","steps":[{"op":"exec","cmd":[" "],"as":"out"}]}]}`,
		"templated prog": `{"tasks":[{"id":"x","params":[{"name":"p","kind":"text"}],"steps":[{"op":"exec","cmd":["{{p}}"],"as":"out"}]}]}`,
		"undefined ref":  `{"tasks":[{"id":"x","steps":[{"op":"exec","cmd":["ls","{{ghost}}"],"as":"out"}]}]}`,
		"cmd elsewhere":  `{"tasks":[{"id":"x","steps":[{"op":"generate","cap":"text","cmd":["ls"],"as":"out"}]}]}`,
	}
	for name, doc := range bad {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The wrapped tools shipped as an example must stay loadable.
func TestExampleToolTasksParse(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", "tools.json"))
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range tasks {
		if tools := tk.Tools(); len(tools) != 1 {
			t.Errorf("%s: tools %v", tk.ID, tools)
		}
	}
}
