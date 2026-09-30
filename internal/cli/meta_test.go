package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgalego/mote/internal/motebook"
)

func cell(note string, first planStage, then ...planStage) metaCell {
	return metaCell{Note: note, plan: plan{First: first, Then: then}}
}

func stg(id string, args ...string) planStage { return planStage{Task: id, Args: args} }

func TestMetaTasksLeavesOutWhatNeedsAnOutputPath(t *testing.T) {
	ids := map[string]bool{}
	for _, tk := range metaTasks(builtins(t)) {
		ids[tk.ID] = true
	}
	if !ids["chat"] || !ids["transcribe"] {
		t.Error("ordinary tasks missing")
	}
	if ids["convert"] {
		t.Error("convert needs -o, which a cell cannot give")
	}
}

func TestMetaSchemaLetsACellNameAnEarlierOne(t *testing.T) {
	schema := metaSchema(metaTasks(builtins(t)), nil)
	var s struct {
		Properties struct {
			Title map[string]any `json:"title"`
			Cells struct {
				MinItems float64 `json:"minItems"`
				MaxItems float64 `json:"maxItems"`
				Items    struct {
					Properties struct {
						First struct {
							AnyOf []map[string]any `json:"anyOf"`
						} `json:"first"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"cells"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schema), &s); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	if s.Properties.Cells.MinItems != 1 || s.Properties.Cells.MaxItems != maxMetaCells {
		t.Errorf("cells bounds %v..%v", s.Properties.Cells.MinItems, s.Properties.Cells.MaxItems)
	}
	// With no file named, a task that takes a file can only be given an
	// earlier cell's output.
	items, ok := offers(s.Properties.Cells.Items.Properties.First.AnyOf, "describe", 1)
	if !ok {
		t.Fatal("describe not offered")
	}
	enum := items[0].(map[string]any)["enum"].([]any)
	if len(enum) != maxMetaCells || enum[0] != "{{step1}}" {
		t.Errorf("file argument %v", enum)
	}
	// The note is written before the stages it describes.
	if i, j := strings.Index(schema, `"note"`), strings.Index(schema, `"first"`); i < 0 || i > j {
		t.Error("note does not come first")
	}
}

func TestBuildNotebookWritesCellsThatReadBackTheSame(t *testing.T) {
	tasks := metaTasks(builtins(t))
	np := metaPlan{
		Title: "Two `steps`\nof it",
		Cells: []metaCell{
			cell("Explain it.\n```mote", stg("chat", "What is a mutex?")),
			cell("", stg("chat", "Shorten: {{step1}}"), stg("chat", `then "quoted" {}`)),
			cell("Retell it.", stg("chat", "Once more: {{step1}}")),
		},
	}
	got, err := buildNotebook("explain `it`\nin two ways", np, tasks)
	if err != nil {
		t.Fatal(err)
	}
	want := "# Two 'steps' of it\n\n> explain 'it' in two ways\n\n" +
		"Explain it. '''mote\n\n```mote as=step1\nchat \"What is a mutex?\"\n```\n\n" +
		"```mote\nchat \"Shorten: {{step1}}\" | chat \"then \\\"quoted\\\" {}\"\n```\n\n" +
		"Retell it.\n\n```mote\nchat \"Once more: {{step1}}\"\n```\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	// Only a cell that is read gets a name.
	book, err := motebook.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if n := book.Cells[0].Name + "," + book.Cells[1].Name + "," + book.Cells[2].Name; n != "step1,," {
		t.Errorf("names %q", n)
	}
}

func TestBuildNotebookRefusesWhatCannotRun(t *testing.T) {
	tasks := metaTasks(builtins(t))
	cases := []struct {
		name string
		np   metaPlan
		want string
	}{
		{"no cells", metaPlan{}, "no cells"},
		{"too many", metaPlan{Cells: make([]metaCell, maxMetaCells+1)}, "at most"},
		{"reads a later cell", metaPlan{Cells: []metaCell{
			cell("", stg("chat", "{{step2}}")), cell("", stg("chat", "x")),
		}}, "cell 1: stage 1: chat uses {{step2}}"},
		{"reads itself", metaPlan{Cells: []metaCell{cell("", stg("chat", "{{step1}}"))}}, "no earlier cell produces"},
		{"unknown task", metaPlan{Cells: []metaCell{cell("", stg("nosuchtask", "x"))}}, "not a task"},
		{"an argument that ends the cell", metaPlan{Cells: []metaCell{
			cell("", stg("chat", "a\n```\nb")),
		}}, "cannot be written"},
		{"an argument that splits the cell", metaPlan{Cells: []metaCell{
			cell("", stg("chat", `x" | chat "y`)),
		}}, "cannot be written"},
	}
	for _, c := range cases {
		if _, err := buildNotebook("goal", c.np, tasks); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

// metaReply is a script line for the fake model: the plan as JSON.
func metaReply(t *testing.T, np metaPlan) string {
	t.Helper()
	return np.JSON()
}

var mutexPlan = metaPlan{Title: "Mutexes", Cells: []metaCell{
	cell("Explain what a mutex is.", stg("chat", "What is a mutex?")),
	cell("Retell it for a child.", stg("chat", "Explain to a child: {{step1}}")),
}}

func TestMetaWritesAndRunsAMotebook(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	log := script(t, metaReply(t, mutexPlan))
	dir := t.TempDir()
	path := filepath.Join(dir, "mutex.mote.md")

	code, out, errs := e.mote("", "meta", "explain a mutex, then retell it", "-o", path)
	if code != 0 || out != "" {
		t.Fatalf("meta: %d %q %s", code, out, errs)
	}
	want := "# Mutexes\n\n> explain a mutex, then retell it\n\n" +
		"Explain what a mutex is.\n\n```mote as=step1\nchat \"What is a mutex?\"\n```\n\n" +
		"Retell it for a child.\n\n```mote\nchat \"Explain to a child: {{step1}}\"\n```\n"
	if got := readFile(t, path); got != want {
		t.Errorf("notebook:\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(errs, "wrote "+path) || !strings.Contains(errs, "mote nb run") {
		t.Errorf("stderr: %s", errs)
	}
	reqs := requests(t, log)
	if len(reqs) != 1 || reqs[0]["format"] == nil {
		t.Fatalf("want one constrained request, got %v", reqs)
	}
	prompt := reqs[0]["prompt"].(string)
	if !strings.Contains(prompt, "Goal: explain a mutex, then retell it") || !strings.Contains(prompt, "chat PROMPT") {
		t.Errorf("prompt lacks the goal or the tasks:\n%s", prompt)
	}
	if strings.Contains(prompt, "convert ") {
		t.Error("the planner was offered a task that needs an output path")
	}

	// What it wrote runs.
	if code, _, errs := e.mote("", "nb", "run", path); code != 0 {
		t.Fatalf("nb run: %d %s", code, errs)
	}
	if got := readFile(t, path); !strings.Contains(got, "echo: Explain to a child: echo: What is a mutex?") {
		t.Errorf("second cell did not read the first:\n%s", got)
	}
}

func TestMetaPrintsWithoutOutputFile(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	script(t, metaReply(t, mutexPlan))
	code, out, errs := e.mote("", "meta", "explain a mutex", "-o", "-")
	if code != 0 || !strings.HasPrefix(out, "# Mutexes\n") || !strings.Contains(out, "as=step1") {
		t.Errorf("meta: %d %q %s", code, out, errs)
	}
}

func TestMetaRunWritesThenRuns(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	script(t, metaReply(t, mutexPlan))
	path := filepath.Join(t.TempDir(), "m.mote.md")
	code, _, errs := e.mote("", "meta", "explain a mutex", "-o", path, "--run")
	if code != 0 {
		t.Fatalf("meta --run: %d %s", code, errs)
	}
	got := readFile(t, path)
	if strings.Count(got, "```output key=") != 2 || !strings.Contains(got, "echo: Explain to a child: echo: What is a mutex?") {
		t.Errorf("notebook was not run:\n%s", got)
	}
}

func TestMetaDoesNotOverwriteWithoutForce(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	log := script(t, metaReply(t, mutexPlan))
	path := filepath.Join(t.TempDir(), "m.mote.md")
	os.WriteFile(path, []byte("mine\n"), 0o644)

	code, _, errs := e.mote("", "meta", "explain a mutex", "-o", path)
	if code != ExitUsage || !strings.Contains(errs, "--force") || readFile(t, path) != "mine\n" {
		t.Errorf("existing file: %d %s", code, errs)
	}
	if len(requests(t, log)) != 0 {
		t.Error("the model was asked before the file was found to be in the way")
	}
	if code, _, errs := e.mote("", "meta", "explain a mutex", "-o", path, "--force"); code != 0 || !strings.HasPrefix(readFile(t, path), "# Mutexes") {
		t.Errorf("--force: %d %s", code, errs)
	}
}

func TestMetaRejectsABadPlanBeforeWritingAnything(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	script(t, metaReply(t, metaPlan{Cells: []metaCell{cell("", stg("chat", "{{step2}}")), cell("", stg("chat", "x"))}}))
	path := filepath.Join(t.TempDir(), "m.mote.md")
	code, _, errs := e.mote("", "meta", "a goal", "-o", path)
	if code == 0 || !strings.Contains(errs, "the plan cannot run") || !strings.Contains(errs, "cell 1") {
		t.Errorf("bad plan: %d %s", code, errs)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a notebook was written for a plan that cannot run")
	}
}

func TestMetaRejectsAReplyThatIsNotAPlan(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	script(t, `not json`)
	if code, _, errs := e.mote("", "meta", "a goal"); code == 0 || !strings.Contains(errs, "planning") {
		t.Errorf("garbage reply: %d %s", code, errs)
	}
}

func TestMetaUsage(t *testing.T) {
	e := newEnv(t)
	e.setup()
	for name, args := range map[string][]string{
		"no goal":          {"meta"},
		"--run without -o": {"meta", "a goal", "--run"},
		"unknown flag":     {"meta", "a goal", "--bogus"},
	} {
		if code, _, _ := e.mote("", args...); code != ExitUsage {
			t.Errorf("%s: exit %d, want usage", name, code)
		}
	}
}

func TestMetaOffersFilesTheGoalNames(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	dir := t.TempDir()
	audio := filepath.Join(dir, "talk.mp3")
	os.WriteFile(audio, nil, 0o644)
	reply := metaReply(t, metaPlan{Title: "Talk", Cells: []metaCell{
		cell("Transcribe it.", stg("transcribe", audio)),
	}})
	log := script(t, reply)

	code, out, errs := e.mote("", "meta", "summarise "+audio)
	if code != 0 || !strings.Contains(out, "transcribe "+pipeQuote(audio)) {
		t.Fatalf("meta: %d %q %s", code, out, errs)
	}
	reqs := requests(t, log)
	if len(reqs) != 1 || !strings.Contains(reqs[0]["prompt"].(string), "Files named in the goal: "+audio) {
		t.Errorf("the planner was not shown the file: %v", reqs)
	}
	// A file the plan invents is caught before anything is written.
	script(t, metaReply(t, metaPlan{Cells: []metaCell{cell("", stg("transcribe", filepath.Join(dir, "ghost.mp3")))}}))
	if code, _, errs := e.mote("", "meta", "summarise "+audio); code == 0 || !strings.Contains(errs, "does not exist") {
		t.Errorf("invented file: %d %s", code, errs)
	}
}

// With --chat the plan is shown and can be changed before anything is
// written. The input stands in for a terminal, so MOTE_FORCE_LIVE.

var threeStepPlan = metaPlan{Title: "Mutexes, three ways", Cells: []metaCell{
	cell("Explain what a mutex is.", stg("chat", "What is a mutex?")),
	cell("Retell it for a child.", stg("chat", "Explain to a child: {{step1}}")),
	cell("Give a Go example.", stg("chat", "Show a Go example of: {{step1}}")),
}}

func chatEnv(t *testing.T, replies ...metaPlan) (*env, string) {
	t.Helper()
	t.Setenv("MOTE_FORCE_LIVE", "1")
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	lines := make([]string, len(replies))
	for i, r := range replies {
		lines[i] = metaReply(t, r)
	}
	return e, script(t, lines...)
}

func TestMetaChatRevisesThePlanBeforeWritingIt(t *testing.T) {
	e, log := chatEnv(t, mutexPlan, threeStepPlan)
	path := filepath.Join(t.TempDir(), "m.mote.md")

	code, out, errs := e.mote("add a third step with a Go example\n\n", "meta", "explain a mutex", "--chat", "-o", path)
	if code != 0 || out != "" {
		t.Fatalf("meta --chat: %d %q %s", code, out, errs)
	}
	got := readFile(t, path)
	if !strings.HasPrefix(got, "# Mutexes, three ways\n") || strings.Count(got, "```mote") != 3 {
		t.Errorf("the revised plan was not written:\n%s", got)
	}
	// Both plans were shown before the choice was made.
	if !strings.Contains(errs, "# Mutexes\n") || !strings.Contains(errs, "# Mutexes, three ways") || !strings.Contains(errs, "change something?") {
		t.Errorf("preview:\n%s", errs)
	}
	reqs := requests(t, log)
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want the plan and one revision", len(reqs))
	}
	revise := reqs[1]["prompt"].(string)
	for _, want := range []string{"Goal: explain a mutex", "Current plan:\n" + mutexPlan.JSON(), "Change requested: add a third step with a Go example"} {
		if !strings.Contains(revise, want) {
			t.Errorf("the revision was not shown %q:\n%s", want, revise)
		}
	}
	if reqs[1]["format"] == nil {
		t.Error("the revision was not constrained to a plan")
	}
}

func TestMetaChatKeepsTheLastGoodPlanWhenARevisionFails(t *testing.T) {
	bad := metaPlan{Cells: []metaCell{cell("", stg("chat", "{{step2}}")), cell("", stg("chat", "x"))}}
	e, log := chatEnv(t, mutexPlan, bad)
	path := filepath.Join(t.TempDir(), "m.mote.md")

	code, _, errs := e.mote("make it worse\n\n", "meta", "explain a mutex", "--chat", "-o", path)
	if code != 0 || !strings.Contains(errs, "the revised plan cannot run") || !strings.Contains(errs, "keeping the last plan") {
		t.Fatalf("bad revision: %d %s", code, errs)
	}
	if got := readFile(t, path); !strings.HasPrefix(got, "# Mutexes\n") || strings.Count(got, "```mote") != 2 {
		t.Errorf("the last good plan was lost:\n%s", got)
	}
	if len(requests(t, log)) != 2 {
		t.Error("the revision was not asked for")
	}
}

func TestMetaChatKeepsThePlanWhenTheModelIsNotAPlan(t *testing.T) {
	t.Setenv("MOTE_FORCE_LIVE", "1")
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	script(t, metaReply(t, mutexPlan), "not json")
	path := filepath.Join(t.TempDir(), "m.mote.md")
	code, _, errs := e.mote("change it\n\n", "meta", "explain a mutex", "--chat", "-o", path)
	if code != 0 || !strings.Contains(errs, "planning:") || !strings.HasPrefix(readFile(t, path), "# Mutexes\n") {
		t.Errorf("garbage revision: %d %s", code, errs)
	}
}

func TestMetaChatCanBeGivenUp(t *testing.T) {
	e, _ := chatEnv(t, mutexPlan)
	path := filepath.Join(t.TempDir(), "m.mote.md")
	code, out, errs := e.mote("q\n", "meta", "explain a mutex", "--chat", "-o", path)
	if code != 0 || out != "" || !strings.Contains(errs, "nothing written") {
		t.Errorf("q: %d %q %s", code, out, errs)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a notebook was written after q")
	}
}

func TestMetaChatPrintsTheResultOnStdout(t *testing.T) {
	e, _ := chatEnv(t, mutexPlan, threeStepPlan)
	// Running out of input keeps what there is, as Enter does.
	code, out, errs := e.mote("one more step please\n", "meta", "explain a mutex", "--chat")
	if code != 0 || !strings.HasPrefix(out, "# Mutexes, three ways\n") || strings.Count(out, "```mote") != 3 {
		t.Fatalf("stdout: %d %q %s", code, out, errs)
	}
	if strings.Count(out, "# Mutexes") != 1 {
		t.Errorf("stdout holds more than the result:\n%s", out)
	}
}

func TestMetaChatSaysWhenAChangeChangesNothing(t *testing.T) {
	e, _ := chatEnv(t, mutexPlan, mutexPlan)
	code, _, errs := e.mote("same again\n\n", "meta", "explain a mutex", "--chat")
	if code != 0 || !strings.Contains(errs, "no change") {
		t.Errorf("no change: %d %s", code, errs)
	}
}

func TestMetaChatCanRunTheResult(t *testing.T) {
	e, _ := chatEnv(t, mutexPlan, threeStepPlan)
	path := filepath.Join(t.TempDir(), "m.mote.md")
	code, _, errs := e.mote("add a step\n\n", "meta", "explain a mutex", "--chat", "-o", path, "--run")
	if code != 0 {
		t.Fatalf("meta --chat --run: %d %s", code, errs)
	}
	if got := readFile(t, path); strings.Count(got, "```output key=") != 3 {
		t.Errorf("the revised notebook was not run:\n%s", got)
	}
}

func TestMetaChatNeedsATerminal(t *testing.T) {
	e := newEnv(t)
	e.setup()
	code, _, errs := e.mote("", "meta", "a goal", "--chat")
	if code != ExitUsage || !strings.Contains(errs, "not a terminal") {
		t.Errorf("--chat without a terminal: %d %s", code, errs)
	}
}

func TestBuildNotebookRefusesSeveralFilesWhereOneIsNeeded(t *testing.T) {
	tasks := metaTasks(builtins(t))
	dir := t.TempDir()
	video := filepath.Join(dir, "clip.mp4")
	os.WriteFile(video, nil, 0o644)
	np := metaPlan{Cells: []metaCell{
		cell("", stg("frames", video, "4")),
		cell("", stg("describe", "{{step1}}")),
	}}
	if _, err := buildNotebook("goal", np, tasks); err == nil || !strings.Contains(err.Error(), "is several") {
		t.Errorf("%v", err)
	}
	// Read as text, several paths are fine.
	np.Cells[1] = cell("", stg("chat", "list these: {{step1}}"))
	if _, err := buildNotebook("goal", np, tasks); err != nil {
		t.Errorf("as text: %v", err)
	}
}

func TestPlainLineCannotOpenAFence(t *testing.T) {
	for in, want := range map[string]string{
		"~~~ tilde note": `\~~~ tilde note`,
		"```code":        "'''code",
		"a ~~~ b":        "a ~~~ b",
	} {
		if got := plainLine(in); got != want {
			t.Errorf("plainLine(%q) = %q, want %q", in, got, want)
		}
	}
	np := metaPlan{Cells: []metaCell{cell("~~~ tilde note", stg("chat", "hi"))}}
	if _, err := buildNotebook("goal", np, metaTasks(builtins(t))); err != nil {
		t.Errorf("a note starting with tildes: %v", err)
	}
}
