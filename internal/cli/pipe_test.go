package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/jgalego/mote/internal/task"
)

func TestSplitArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`chat hello`, []string{"chat", "hello"}},
		{`chat "two words"`, []string{"chat", "two words"}},
		{`chat 'single quoted'`, []string{"chat", "single quoted"}},
		{`chat "it's fine"`, []string{"chat", "it's fine"}},
		{`chat ""`, []string{"chat", ""}},
		{`chat a\ b`, []string{"chat", "a b"}},
		{`  spaced   out  `, []string{"spaced", "out"}},
	}
	for _, c := range cases {
		got, err := splitArgs(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q want %q", c.in, got, c.want)
		}
	}
	if _, err := splitArgs(`chat "unclosed`); err == nil {
		t.Error("unclosed quote accepted")
	}
}

func TestParsePipeline(t *testing.T) {
	stages, err := parsePipeline(`transcribe a.m4a | chat "summarise: {}" | !tee out.txt`)
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 3 {
		t.Fatalf("got %d stages", len(stages))
	}
	if stages[0].id != "transcribe" || !reflect.DeepEqual(stages[0].args, []string{"a.m4a"}) {
		t.Errorf("stage 0: %+v", stages[0])
	}
	if stages[1].id != "chat" || !reflect.DeepEqual(stages[1].args, []string{"summarise: {}"}) {
		t.Errorf("stage 1: %+v", stages[1])
	}
	if stages[2].shell != "tee out.txt" {
		t.Errorf("stage 2: %+v", stages[2])
	}

	// A | inside quotes belongs to the argument, not the pipeline.
	stages, err = parsePipeline(`chat "a | b" | chat -`)
	if err != nil || len(stages) != 2 || stages[0].args[0] != "a | b" {
		t.Errorf("quoted pipe: %v %+v", err, stages)
	}

	for _, bad := range []string{`chat hello`, `chat a || chat b`, `chat a | `, `chat a | !`} {
		if _, err := parsePipeline(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestBindPlacesThePipedValue(t *testing.T) {
	chat := task.Task{ID: "chat", Params: []task.Param{{Name: "prompt", Kind: "text"}}}
	describe := task.Task{ID: "describe", Params: []task.Param{
		{Name: "image", Kind: "file"}, {Name: "question", Kind: "text"},
	}}

	// The first stage takes its arguments as written.
	got, err := bind(chat, []string{"hello"}, task.Value{Text: "ignored"}, true)
	if err != nil || !reflect.DeepEqual(got, [][]string{{"hello"}}) {
		t.Errorf("first stage: %q %v", got, err)
	}

	// {} and - are replaced by the value.
	got, err = bind(chat, []string{"summarise: {}"}, task.Value{Text: "a transcript"}, false)
	if err != nil || !reflect.DeepEqual(got, [][]string{{"summarise: a transcript"}}) {
		t.Errorf("marker: %q %v", got, err)
	}
	got, err = bind(chat, []string{"-"}, task.Value{Text: "a transcript"}, false)
	if err != nil || !reflect.DeepEqual(got, [][]string{{"a transcript"}}) {
		t.Errorf("dash: %q %v", got, err)
	}

	// With no marker the value fills the first unset parameter.
	got, err = bind(describe, nil, task.Value{Files: []string{"a.png"}}, false)
	if err != nil || !reflect.DeepEqual(got, [][]string{{"a.png"}}) {
		t.Errorf("free param: %q %v", got, err)
	}

	// Several files fan out into one run each.
	got, err = bind(describe, []string{}, task.Value{Files: []string{"a.png", "b.png"}}, false)
	if err != nil || !reflect.DeepEqual(got, [][]string{{"a.png"}, {"b.png"}}) {
		t.Errorf("fan out: %q %v", got, err)
	}

	// Text cannot stand in for a file, and a full argument list has no room.
	if _, err := bind(describe, nil, task.Value{Text: "words"}, false); err == nil {
		t.Error("text accepted for a file parameter")
	}
	if _, err := bind(chat, []string{"hello"}, task.Value{Text: "x"}, false); err == nil {
		t.Error("full argument list accepted without a marker")
	} else if !strings.Contains(err.Error(), "{}") {
		t.Errorf("error should suggest the marker: %v", err)
	}
}

func TestPipeRunsStages(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")

	// chat echoes its prompt, so the second stage sees the first's output.
	code, out, errs := e.mote("", "pipe", `chat hello | chat "again: {}"`)
	if code != 0 || !strings.Contains(out, "again: echo: hello") {
		t.Fatalf("two tasks: %d %q %s", code, out, errs)
	}

	// A bare - means the same as {}.
	if code, out, _ := e.mote("", "pipe", `chat hello | chat -`); code != 0 || !strings.Contains(out, "echo: echo: hello") {
		t.Errorf("dash: %d %q", code, out)
	}

	// The last stage's -o writes the file, and nothing is highlighted there.
	dir := t.TempDir()
	f := filepath.Join(dir, "out.txt")
	if code, _, errs := e.mote("", "pipe", `chat hello | chat -`, "-o", f); code != 0 {
		t.Fatalf("pipe -o: %d %s", code, errs)
	}
	if b, _ := os.ReadFile(f); !strings.Contains(string(b), "echo: echo: hello") {
		t.Errorf("file holds %q", b)
	}
}

// Shell stages use the system shell, so keep to builtins: the test
// environment has an empty PATH, and cmd.exe is a different language.
func TestPipeShellStages(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stages use cmd /c on Windows; the sh syntax here does not apply")
	}
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")

	// The value arrives on stdin and the command's output carries on.
	code, out, errs := e.mote("", "pipe", `chat hello | !read l; echo "shell saw [$l]" | chat -`)
	if code != 0 || !strings.Contains(out, "shell saw [echo: hello]") {
		t.Fatalf("shell stage: %d %q %s", code, out, errs)
	}
	// A pipeline may also end with a shell stage.
	if code, out, _ := e.mote("", "pipe", `chat hello | !read l; echo "last:$l"`); code != 0 || !strings.Contains(out, "last:echo: hello") {
		t.Errorf("final shell stage: %d %q", code, out)
	}
	// A failing command stops the pipeline.
	if code, _, _ := e.mote("", "pipe", `chat hello | !exit 3 | chat -`); code == 0 {
		t.Error("failed shell stage did not stop the pipeline")
	}
}

func TestPipeReportsBadPipelinesBeforeRunning(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")

	// An unknown task in the last stage fails before the first one runs.
	code, _, errs := e.mote("", "pipe", `chat hello | nosuchtask`)
	if code != ExitUsage || !strings.Contains(errs, "nosuchtask") {
		t.Errorf("unknown task: %d %s", code, errs)
	}
	if code, _, _ := e.mote("", "pipe", `chat hello`); code != ExitUsage {
		t.Errorf("single stage should be a usage error: %d", code)
	}
	if code, _, _ := e.mote("", "pipe"); code != ExitUsage {
		t.Errorf("no expression: %d", code)
	}
	code, _, errs = e.mote("", "pipe", `chat hello | chat there`)
	if code != ExitUsage || !strings.Contains(errs, "{}") {
		t.Errorf("no room for the value: %d %s", code, errs)
	}
}

func TestPipeStopsWhenAStageProducesNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stages use cmd /c on Windows")
	}
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")

	// Without this, the next task is asked to comment on nothing and the
	// model invents an answer.
	code, out, errs := e.mote("", "pipe", `chat hello | !printf '' | chat "about: {}"`)
	if code == 0 {
		t.Errorf("empty stage was passed on: %q %s", out, errs)
	}
	if !strings.Contains(errs, "produced nothing") || !strings.Contains(errs, "stage 2") {
		t.Errorf("unhelpful error: %s", errs)
	}
	// An empty *last* stage is fine: there is nothing after it to mislead.
	if code, _, errs := e.mote("", "pipe", `chat hello | !printf ''`); code != 0 {
		t.Errorf("empty final stage: %d %s", code, errs)
	}
}
