package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jgalego/mote/internal/task"
)

func TestPositionalMapsNamedArguments(t *testing.T) {
	tasks := builtins(t)
	find := func(id string) task.Task { tk, _ := task.Find(tasks, id); return tk }

	cases := []struct {
		task  string
		named map[string]string
		want  []string
	}{
		{"chat", map[string]string{"prompt": "hi"}, []string{"hi"}},
		// An empty optional is left out, and a default fills in for it.
		{"extract", map[string]string{"file": "a.txt", "schema": ""}, []string{"a.txt"}},
		{"describe", map[string]string{"image": "p.png", "question": ""}, []string{"p.png", "Describe this image in two sentences."}},
		{"describe", map[string]string{"image": "p.png", "question": "Who?"}, []string{"p.png", "Who?"}},
	}
	for _, c := range cases {
		got, err := positional(find(c.task), c.named)
		if err != nil || strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s %v: %q %v, want %q", c.task, c.named, got, err, c.want)
		}
	}

	if _, err := positional(find("chat"), map[string]string{"prompt": " "}); err == nil || !strings.Contains(err.Error(), "needs prompt") {
		t.Errorf("blank required argument: %v", err)
	}
	if _, err := positional(find("chat"), map[string]string{"prompt": "x", "temperature": "9"}); err == nil || !strings.Contains(err.Error(), "temperature") {
		t.Errorf("unknown argument: %v", err)
	}
	// A later argument cannot follow a gap nothing can fill.
	gappy := task.Task{ID: "g", Params: []task.Param{{Name: "a", Kind: "text"},
		{Name: "b", Kind: "text", Optional: true}, {Name: "c", Kind: "text", Optional: true}}}
	if _, err := positional(gappy, map[string]string{"a": "x", "b": "", "c": "y"}); err == nil {
		t.Error("a gap before a given argument was accepted")
	}
}

func TestStepSchemaAsksForAThoughtFirst(t *testing.T) {
	tasks := builtins(t)
	chat, _ := task.Find(tasks, "chat")
	a := &app{}
	tools := []agentTool{a.taskTool(chat, task.Env{})}
	schema := stepSchema(tools, false)
	if !json.Valid([]byte(schema)) {
		t.Fatal("schema is not JSON")
	}
	if strings.Index(schema, `"thought"`) > strings.Index(schema, `"action"`) {
		t.Error("the action comes before the thought")
	}
	if strings.Index(schema, `"tool"`) > strings.Index(schema, `"args"`) {
		t.Error("args come before the tool")
	}
	for _, want := range []string{`"const":"chat"`, `"const":"finish"`, `"maxLength":200`, `"maxLength":2000`, `"prompt"`} {
		if !strings.Contains(schema, want) {
			t.Errorf("schema lacks %s: %s", want, schema)
		}
	}
	final := stepSchema(tools, true)
	if strings.Contains(final, `"const":"chat"`) || !strings.Contains(final, `"const":"finish"`) {
		t.Errorf("out of steps, only finish should be allowed: %s", final)
	}
}

func TestClipKeepsBothEnds(t *testing.T) {
	if got := clip("  short  ", 100); got != "short" {
		t.Errorf("short text changed: %q", got)
	}
	long := "START" + strings.Repeat("x", 5000) + "END"
	got := clip(long, 300)
	if !strings.HasPrefix(got, "START") || !strings.HasSuffix(got, "END") || !strings.Contains(got, "characters cut") {
		t.Errorf("clip lost an end or the note: %.80q…", got)
	}
	if len(got) > 360 {
		t.Errorf("clipped to %d bytes", len(got))
	}
	// Multi-byte text is never split inside a character.
	accents := strings.Repeat("é", 1000)
	if got := clip(accents, 301); !utf8.ValidString(got) {
		t.Error("clip produced invalid UTF-8")
	}
}

func TestAgentPromptShortensOlderObservations(t *testing.T) {
	obs := func(tag string) string { return tag + strings.Repeat(".", 3000) + tag + "-end" }
	history := []taken{
		{"t1", "chat", `{"prompt":"a"}`, obs("one")},
		{"t2", "chat", `{"prompt":"b"}`, obs("two")},
		{"t3", "chat", `{"prompt":"c"}`, obs("three")},
		{"t4", "chat", `{"prompt":"d"}`, obs("four")},
	}
	p := agentPrompt("the goal", "/work", nil, history, 3, false)
	for _, want := range []string{"Working directory: /work", "Goal: the goal", "Step 4\nThought: t4", "Action: chat {\"prompt\":\"d\"}", "3 steps left", "finish(answer)"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	section := func(n string) string {
		start := strings.Index(p, "Step "+n+"\n")
		end := strings.Index(p[start+1:], "\nStep ")
		if end < 0 {
			return p[start:]
		}
		return p[start : start+1+end]
	}
	if len(section("1")) > oldObservation+200 || len(section("2")) > oldObservation+200 {
		t.Error("older observations were not shortened")
	}
	if len(section("4")) < maxObservation {
		t.Error("the newest observation was shortened too far")
	}
	if !strings.Contains(agentPrompt("g", "/", nil, history, 0, true), "No steps are left") {
		t.Error("the final prompt does not ask for an answer")
	}
}

func TestAgentToolsChoice(t *testing.T) {
	tasks := builtins(t)
	a := &app{}
	ask := func(string) bool { return true }
	ids := func(tools []agentTool) string {
		var out []string
		for _, t := range tools {
			out = append(out, t.id)
		}
		return strings.Join(out, ",")
	}

	tools, err := a.agentTools(tasks, "", false, task.Env{}, ask, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := ids(tools)
	if !strings.Contains(got, "chat") || strings.Contains(got, "speak") || strings.Contains(got, "convert") || strings.Contains(got, "sh") {
		t.Errorf("default tools %s: tasks writing files and sh should be left out", got)
	}
	tools, _ = a.agentTools(tasks, "", true, task.Env{}, ask, nil)
	if !strings.Contains(ids(tools), ",sh") {
		t.Errorf("--allow-sh did not add sh: %s", ids(tools))
	}
	tools, err = a.agentTools(tasks, "code, chat,chat", false, task.Env{}, ask, nil)
	if err != nil || ids(tools) != "chat,code" {
		t.Errorf("--tools: %s %v", ids(tools), err)
	}
	for names, want := range map[string]string{
		"chat,sh":  "--allow-sh",
		"teleport": "unknown tool",
		"convert":  "needs -o",
		",":        "no tools",
	} {
		if _, err := a.agentTools(tasks, names, false, task.Env{}, ask, nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("--tools %q: %v, want %q", names, err, want)
		}
	}
}

// constrained returns the schema-constrained requests from a fake model log:
// the agent's steps, as opposed to the tools it ran.
func constrained(t *testing.T, log string) []map[string]any {
	var out []map[string]any
	for _, r := range requests(t, log) {
		if r["format"] != nil {
			out = append(out, r)
		}
	}
	return out
}

func agentEnv(t *testing.T) *env {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	return e
}

func TestAgentCallsAToolAndFinishes(t *testing.T) {
	e := agentEnv(t)
	log := script(t,
		`{"thought":"ask chat","action":{"tool":"chat","args":{"prompt":"hello"}}}`,
		`{"thought":"got it","action":{"tool":"finish","args":{"answer":"the answer"}}}`)
	code, out, errs := e.mote("", "agent", "say hello")
	if code != 0 {
		t.Fatalf("agent: %d %s", code, errs)
	}
	if out != "the answer\n" {
		t.Errorf("stdout %q should hold the answer alone", out)
	}
	for _, want := range []string{"step 1", "ask chat", `chat {"prompt":"hello"}`, "echo: hello", "step 2", "small for an agent"} {
		if !strings.Contains(errs, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errs)
		}
	}
	steps := constrained(t, log)
	if len(steps) != 2 {
		t.Fatalf("%d steps asked for, want 2", len(steps))
	}
	if p := steps[1]["prompt"].(string); !strings.Contains(p, "Observation: echo: hello") {
		t.Errorf("the second step did not see the first one's result:\n%s", p)
	}
	if s := steps[0]["system"].(string); !strings.Contains(s, "one tool") {
		t.Errorf("system prompt %q", s)
	}
}

func TestAgentTurnsErrorsIntoObservations(t *testing.T) {
	e := agentEnv(t)
	log := script(t,
		`{"thought":"t","action":{"tool":"extract","args":{"file":"ghost.txt","schema":""}}}`,
		`{"thought":"t","action":{"tool":"teleport","args":{}}}`,
		`{"thought":"t","action":{"tool":"chat","args":{"prompt":""}}}`,
		`{"thought":"t","action":{"tool":"finish","args":{"answer":"gave up"}}}`)
	code, out, errs := e.mote("", "agent", "read ghost.txt")
	if code != 0 || out != "gave up\n" {
		t.Fatalf("agent: %d %q %s", code, out, errs)
	}
	last := constrained(t, log)[3]["prompt"].(string)
	for _, want := range []string{"Observation: error: ", "stat ghost.txt", `no tool named "teleport"`, "chat needs prompt"} {
		if !strings.Contains(last, want) {
			t.Errorf("observations lack %q:\n%s", want, last)
		}
	}
}

func TestAgentStopsARepeatingModel(t *testing.T) {
	e := agentEnv(t)
	same := `{"thought":"again","action":{"tool":"chat","args":{"prompt":"hi"}}}`
	log := script(t, same, same, strings.Replace(same, `"hi"`, ` "hi" `, 1),
		`{"thought":"fine","action":{"tool":"finish","args":{"answer":"stopped"}}}`)
	code, out, errs := e.mote("", "agent", "loop forever")
	if code != 0 || out != "stopped\n" {
		t.Fatalf("agent: %d %q %s", code, out, errs)
	}
	if n := strings.Count(errs, "echo: hi"); n != 1 {
		t.Errorf("the repeated call ran %d times, want once", n)
	}
	steps := constrained(t, log)
	if len(steps) != 4 {
		t.Fatalf("%d constrained requests, want 3 steps and a final answer", len(steps))
	}
	if !strings.Contains(steps[2]["prompt"].(string), "already made this call at step 1") {
		t.Error("the first repeat was not pointed out")
	}
	final := steps[3]
	if !strings.Contains(final["prompt"].(string), "No steps are left") || strings.Contains(string(mustMarshal(final["format"])), `"const":"chat"`) {
		t.Error("the stuck model was not limited to an answer")
	}
}

func mustMarshal(v any) []byte { b, _ := json.Marshal(v); return b }

func TestAgentStepLimit(t *testing.T) {
	e := agentEnv(t)
	log := script(t,
		`{"thought":"a","action":{"tool":"chat","args":{"prompt":"one"}}}`,
		`{"thought":"b","action":{"tool":"chat","args":{"prompt":"two"}}}`,
		`{"thought":"c","action":{"tool":"finish","args":{"answer":"best effort"}}}`)
	code, out, errs := e.mote("", "agent", "--steps", "2", "count")
	if code != 0 || out != "best effort\n" {
		t.Fatalf("agent: %d %q %s", code, out, errs)
	}
	if n := len(constrained(t, log)); n != 3 {
		t.Errorf("%d constrained requests, want 2 steps and a final answer", n)
	}

	// A model that will not answer even then is an error, not an empty
	// answer.
	script(t, `{"thought":"a","action":{"tool":"chat","args":{"prompt":"x"}}}`)
	if code, _, errs := e.mote("", "agent", "--steps", "1", "count"); code == 0 || !strings.Contains(errs, "did not give an answer") {
		t.Errorf("no answer: %d %s", code, errs)
	}
}

func TestAgentShellAsksFirst(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the commands are POSIX shell")
	}
	e := agentEnv(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	run := `{"thought":"t","action":{"tool":"sh","args":{"command":"echo out; echo oops >&2; echo x > ` + marker + `; exit 3"}}}`
	done := `{"thought":"t","action":{"tool":"finish","args":{"answer":"ok"}}}`

	// Without a terminal to ask on, sh needs --yes.
	if code, _, errs := e.mote("", "agent", "--allow-sh", "x"); code != ExitUsage || !strings.Contains(errs, "--yes") {
		t.Errorf("sh without a way to ask: %d %s", code, errs)
	}
	// Offered only when allowed.
	script(t, run, done)
	if code, _, errs := e.mote("", "agent", "--tools", "sh", "x"); code != ExitUsage || !strings.Contains(errs, "--allow-sh") {
		t.Errorf("sh without --allow-sh: %d %s", code, errs)
	}

	// Declined at the prompt: nothing runs and the model is told why.
	t.Setenv("MOTE_FORCE_LIVE", "1")
	log := script(t, run, done)
	code, _, errs := e.mote("n\n", "agent", "--allow-sh", "x")
	if code != 0 || !strings.Contains(errs, "[y/N]") {
		t.Fatalf("declined: %d %s", code, errs)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a declined command ran")
	}
	if p := constrained(t, log)[1]["prompt"].(string); !strings.Contains(p, "declined") {
		t.Errorf("the model was not told the command was declined:\n%s", p)
	}

	// Accepted: it runs, and the model sees output, errors and exit status.
	log = script(t, run, done)
	if code, _, errs := e.mote("y\n", "agent", "--allow-sh", "x"); code != 0 {
		t.Fatalf("accepted: %d %s", code, errs)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("an accepted command did not run")
	}
	p := constrained(t, log)[1]["prompt"].(string)
	for _, want := range []string{"exit status 3", "out", "oops"} {
		if !strings.Contains(p, want) {
			t.Errorf("observation lacks %q:\n%s", want, p)
		}
	}
	t.Setenv("MOTE_FORCE_LIVE", "")

	// --yes runs without asking.
	os.Remove(marker)
	script(t, run, done)
	if code, _, errs := e.mote("", "agent", "--allow-sh", "--yes", "x"); code != 0 || strings.Contains(errs, "[y/N]") {
		t.Errorf("--yes: %d %s", code, errs)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("--yes did not run the command")
	}
}

func TestAgentCallsWrappedLocalPrograms(t *testing.T) {
	e := agentEnv(t)
	bin := fakeTools(t)
	os.WriteFile(filepath.Join(bin, "lookup"), []byte("#!/bin/sh\n[ \"$1\" = -- ] && shift\necho \"found: $1\"\n"), 0o755)
	dir := t.TempDir()
	t.Setenv("MOTE_TASKS_DIR", dir)
	os.WriteFile(filepath.Join(dir, "lookup.json"), []byte(`{"tasks":[{"id":"lookup","summary":"Look a word up",
	  "in":["text"],"out":"text","params":[{"name":"word","kind":"text"}],
	  "steps":[{"op":"exec","cmd":["lookup","--","{{word}}"],"as":"out"}]}]}`), 0o644)
	log := script(t,
		`{"thought":"t","action":{"tool":"lookup","args":{"word":"mote"}}}`,
		`{"thought":"t","action":{"tool":"finish","args":{"answer":"done"}}}`)
	if code, _, errs := e.mote("", "agent", "--tools", "lookup,chat", "look up mote"); code != 0 {
		t.Fatalf("agent: %d %s", code, errs)
	}
	steps := constrained(t, log)
	if !strings.Contains(steps[0]["prompt"].(string), "lookup(word)") {
		t.Error("the wrapped program is not offered as a tool")
	}
	if !strings.Contains(steps[1]["prompt"].(string), "Observation: found: mote") {
		t.Errorf("its output did not come back:\n%s", steps[1]["prompt"])
	}
}

func TestAgentUsage(t *testing.T) {
	e := agentEnv(t)
	for _, args := range [][]string{
		{"agent"},
		{"agent", "--steps", "0", "x"},
		{"agent", "--steps", "many", "x"},
		{"agent", "--steps", "31", "x"},
	} {
		if code, _, _ := e.mote("", args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want a usage error", args, code)
		}
	}
}
