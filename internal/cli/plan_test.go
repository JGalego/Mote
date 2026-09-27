package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jgalego/mote/internal/task"
)

func builtins(t *testing.T) []task.Task {
	t.Helper()
	tasks, err := task.Load()
	if err != nil {
		t.Fatal(err)
	}
	return tasks
}

// schemaBranches decodes the first and then alternatives of a plan schema.
func schemaBranches(t *testing.T, schema string) (first, then []map[string]any) {
	t.Helper()
	var s struct {
		Properties struct {
			First struct {
				AnyOf []map[string]any `json:"anyOf"`
			} `json:"first"`
			Then struct {
				Items struct {
					AnyOf []map[string]any `json:"anyOf"`
				} `json:"items"`
			} `json:"then"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schema), &s); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	return s.Properties.First.AnyOf, s.Properties.Then.Items.AnyOf
}

// offers reports whether some branch names the task with n arguments, and
// the argument schemas of the first such branch.
func offers(branches []map[string]any, id string, n int) ([]any, bool) {
	for _, b := range branches {
		props := b["properties"].(map[string]any)
		if props["task"].(map[string]any)["const"] != id {
			continue
		}
		args := props["args"].(map[string]any)
		if int(args["maxItems"].(float64)) != n {
			continue
		}
		items, _ := args["prefixItems"].([]any)
		return items, true
	}
	return nil, false
}

func TestPlanSchemaWritesTheFirstStageFirst(t *testing.T) {
	schema := planSchema(builtins(t), nil)
	if strings.Index(schema, `"first"`) > strings.Index(schema, `"then"`) {
		t.Error("then comes before first")
	}
	// Within a stage the task is chosen before its arguments.
	if strings.Index(schema, `"task"`) > strings.Index(schema, `"args"`) {
		t.Error("args come before task")
	}
}

func TestPlanSchemaOnlyOffersRealFiles(t *testing.T) {
	tasks := builtins(t)
	first, then := schemaBranches(t, planSchema(tasks, nil))
	// With no file in the request, nothing that needs one can run first,
	// and a later stage can only take the previous output.
	if _, ok := offers(first, "transcribe", 1); ok {
		t.Error("transcribe offered first with no file to give it")
	}
	if _, ok := offers(first, "chat", 1); !ok {
		t.Error("chat not offered first")
	}
	// A first stage gives every required argument.
	if _, ok := offers(first, "chat", 0); ok {
		t.Error("chat offered first with no prompt")
	}
	items, ok := offers(then, "transcribe", 1)
	if !ok {
		t.Fatal("transcribe not offered as a later stage")
	}
	if enum := items[0].(map[string]any)["enum"].([]any); len(enum) != 1 || enum[0] != "{}" {
		t.Errorf("later file argument %v, want only {}", enum)
	}
	if _, ok := offers(then, "chat", 0); !ok {
		t.Error("a later chat should be able to take the previous output as its prompt")
	}

	first, _ = schemaBranches(t, planSchema(tasks, []string{"talk.mp3"}))
	items, ok = offers(first, "transcribe", 1)
	if !ok {
		t.Fatal("transcribe not offered for a named file")
	}
	if enum := items[0].(map[string]any)["enum"].([]any); len(enum) != 1 || enum[0] != "talk.mp3" {
		t.Errorf("first file argument %v, want the named file without {}", enum)
	}
	// extract's optional schema is a second file, so it needs two.
	if _, ok := offers(first, "extract", 2); ok {
		t.Error("extract offered a schema file when the request names one file")
	}
	first, _ = schemaBranches(t, planSchema(tasks, []string{"invoice.txt", "schema.json"}))
	if _, ok := offers(first, "extract", 2); !ok {
		t.Error("extract not offered a schema file when two are named")
	}
}

func TestNamedFilesFindsWhatExists(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "talk.mp3")
	os.WriteFile(f, nil, 0o644)
	got := namedFiles(`summarise "` + f + `", and ignore ghost.mp3`)
	if len(got) != 1 || got[0] != f {
		t.Errorf("files %q", got)
	}
}

func TestCheckPlan(t *testing.T) {
	tasks := builtins(t)
	dir := t.TempDir()
	audio := filepath.Join(dir, "talk.mp3")
	os.WriteFile(audio, nil, 0o644)
	st := func(id string, args ...string) planStage { return planStage{Task: id, Args: args} }

	good := plan{First: st("transcribe", audio), Then: []planStage{st("chat", "Summarise: {}")}}
	stages, found, err := checkPlan(good, tasks, "req", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 2 || found[1].ID != "chat" || stages[1].args[0] != "Summarise: {}" {
		t.Errorf("stages %+v", stages)
	}

	bad := map[string]struct {
		p      plan
		output bool
		want   string
	}{
		"unknown task":      {plan{First: st("teleport", "x")}, false, `"teleport", which is not a task`},
		"too many args":     {plan{First: st("chat", "a", "b")}, false, "at most 1"},
		"missing required":  {plan{First: st("refactor", audio)}, false, "missing INSTRUCTION"},
		"invented file":     {plan{First: st("transcribe", filepath.Join(dir, "ghost.mp3"))}, false, "does not exist"},
		"piped file first":  {plan{First: st("transcribe", "{}")}, false, "runs first"},
		"text into a file":  {plan{First: st("code", "a sort"), Then: []planStage{st("doc", "{}")}}, false, "doc needs a file, but code before it produces code"},
		"text fills a file": {plan{First: st("chat", "hi"), Then: []planStage{st("doc")}}, false, "doc needs a file"},
		"no room":           {plan{First: st("transcribe", audio), Then: []planStage{st("doc", audio)}}, false, "no argument left"},
		"file task first":   {plan{First: st("convert", audio), Then: []planStage{st("chat")}}, true, "must come last"},
		"file task no -o":   {plan{First: st("convert", audio)}, false, "needs -o"},
		"too long":          {plan{First: st("chat", "a"), Then: []planStage{st("chat"), st("chat"), st("chat"), st("chat")}}, false, "at most 4"},
	}
	for name, c := range bad {
		if _, _, err := checkPlan(c.p, tasks, "req", c.output); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}

	// A task whose files feed the next is fine: frames produces images.
	vid := filepath.Join(dir, "clip.mp4")
	os.WriteFile(vid, nil, 0o644)
	if _, _, err := checkPlan(plan{First: st("frames", vid), Then: []planStage{st("describe", "{}")}}, tasks, "req", false); err != nil {
		t.Errorf("frames into describe: %v", err)
	}
	if _, _, err := checkPlan(plan{First: st("convert", audio)}, tasks, "req", true); err != nil {
		t.Errorf("a file task last with -o: %v", err)
	}
}

func TestCheckPlanRepairsCommonSlips(t *testing.T) {
	tasks := builtins(t)
	st := func(id string, args ...string) planStage { return planStage{Task: id, Args: args} }

	// {} in the first stage can only mean the request.
	stages, _, err := checkPlan(plan{First: st("chat", "Answer briefly: {}")}, tasks, "what is a mutex", false)
	if err != nil || stages[0].args[0] != "Answer briefly: what is a mutex" {
		t.Errorf("first-stage {}: %q %v", stages[0].args, err)
	}
	stages, _, err = checkPlan(plan{First: st("chat", "-")}, tasks, "what is a mutex", false)
	if err != nil || stages[0].args[0] != "what is a mutex" {
		t.Errorf("first-stage -: %q %v", stages[0].args, err)
	}
	// A later instruction without {} gets the previous output after it,
	// visibly, rather than failing.
	stages, _, err = checkPlan(plan{First: st("code", "reverse a string"), Then: []planStage{st("chat", "Explain it ")}}, tasks, "r", false)
	if err != nil || stages[1].args[0] != "Explain it\n\n{}" {
		t.Errorf("forgotten {}: %q %v", stages[1].args, err)
	}
	if !strings.Contains(stages[1].text, `{}`) {
		t.Errorf("the repair is not visible in %q", stages[1].text)
	}
}

func TestPlanStagesReadBackUnchanged(t *testing.T) {
	nasty := []string{"plain", "two words", `it's "quoted"`, `back\slash`, "a | pipe", "line\nbreak", "", "{}", "tab\there"}
	var texts []string
	for _, a := range nasty {
		texts = append(texts, pipeStage("chat", []string{a}))
	}
	stages, err := parsePipeline(strings.Join(texts, " | "))
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range stages {
		if s.id != "chat" || len(s.args) != 1 || s.args[0] != nasty[i] {
			t.Errorf("%q read back as %q", nasty[i], s.args)
		}
	}
}

func TestPlanCommandIsWhatYouWouldType(t *testing.T) {
	one := []stage{{id: "chat", args: []string{"it's a test"}}}
	if got := planCommand(one); got != `mote run chat 'it'\''s a test'` {
		t.Errorf("single stage: %s", got)
	}
	two := []stage{{id: "transcribe", args: []string{"talk.mp3"}, text: "transcribe talk.mp3"},
		{id: "chat", args: []string{"Sum: {}"}, text: `chat "Sum: {}"`}}
	if got := planCommand(two); got != `mote pipe 'transcribe talk.mp3 | chat "Sum: {}"'` {
		t.Errorf("pipeline: %s", got)
	}
	if runtime.GOOS == "windows" {
		return
	}
	// The quoting survives a real shell.
	for _, s := range []string{"it's", `a "b" $HOME`, "x'y'z"} {
		out, err := exec.Command("/bin/sh", "-c", "printf %s "+shellQuote(s)).Output()
		if err != nil || string(out) != s {
			t.Errorf("%q came back as %q (%v)", s, out, err)
		}
	}
}

// script writes the replies the fake model gives to constrained requests
// and a log of what it was asked, returning the log's path.
func script(t *testing.T, replies ...string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "script")
	if err := os.WriteFile(p, []byte(strings.Join(replies, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOTE_FAKE_SCRIPT", p)
	log := filepath.Join(dir, "log")
	t.Setenv("MOTE_FAKE_LOG", log)
	return log
}

// requests returns what the fake model was asked, in order.
func requests(t *testing.T, log string) []map[string]any {
	t.Helper()
	b, _ := os.ReadFile(log)
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func TestDoPlanRunsThePipeline(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	log := script(t, `{"first":{"task":"chat","args":["first"]},"then":[{"task":"chat","args":["second: {}"]}]}`)

	code, out, errs := e.mote("", "do", "--plan", "--trace", "two answers")
	if code != 0 {
		t.Fatalf("do --plan: %d %s", code, errs)
	}
	if strings.TrimSpace(out) != "echo: second: echo: first" {
		t.Errorf("output %q", out)
	}
	if !strings.Contains(errs, `mote pipe 'chat first | chat "second: {}"'`) {
		t.Errorf("plan not shown: %s", errs)
	}
	if !strings.Contains(errs, "stage 1 (chat)") {
		t.Errorf("--trace did not show the first stage: %s", errs)
	}
	reqs := requests(t, log)
	if len(reqs) != 3 {
		t.Fatalf("%d model requests, want a plan and two stages", len(reqs))
	}
	if reqs[0]["format"] == nil || !strings.Contains(reqs[0]["prompt"].(string), "Request: two answers") {
		t.Errorf("planning request %v", reqs[0])
	}
	if !strings.Contains(reqs[0]["prompt"].(string), "chat PROMPT") {
		t.Error("the planner was not shown the tasks' arguments")
	}
}

func TestDoPlanDryRunAndRefusals(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	log := script(t, `{"first":{"task":"chat","args":["hello"]},"then":[]}`)
	code, out, errs := e.mote("", "do", "--plan", "--dry-run", "say hello")
	if code != 0 || out != "" || !strings.Contains(errs, "mote run chat 'hello'") {
		t.Errorf("dry run: %d %q %s", code, out, errs)
	}
	if n := len(requests(t, log)); n != 1 {
		t.Errorf("dry run made %d requests; only the plan should run", n)
	}

	// A plan naming a file that is not there stops before any stage runs.
	log = script(t, `{"first":{"task":"transcribe","args":["ghost.mp3"]},"then":[]}`)
	code, _, errs = e.mote("", "do", "--plan", "transcribe the meeting")
	if code == 0 || !strings.Contains(errs, "ghost.mp3") || !strings.Contains(errs, "cannot run") {
		t.Errorf("invented file: %d %s", code, errs)
	}
	if n := len(requests(t, log)); n != 1 {
		t.Errorf("a refused plan still ran %d requests", n)
	}

	// A reply that is not a plan is reported, not run.
	script(t, `not json at all`)
	if code, _, errs := e.mote("", "do", "--plan", "anything"); code == 0 || !strings.Contains(errs, "planning") {
		t.Errorf("garbage plan: %d %s", code, errs)
	}

	for _, args := range [][]string{
		{"do", "--plan", "--router", "embed", "x"},
		{"do", "--plan", "--recall", "x"},
		{"do", "--trace", "x"},
	} {
		if code, _, _ := e.mote("", args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want a usage error", args, code)
		}
	}
}
