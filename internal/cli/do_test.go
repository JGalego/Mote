package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jgalego/mote/internal/task"
	"github.com/jgalego/mote/internal/ui"
)

func TestRoutableSkipsTasksARequestCannotDrive(t *testing.T) {
	tasks, err := task.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range routable(tasks) {
		if len(r.Params) == 0 {
			t.Errorf("%s has no parameters", r.ID)
		}
	}
	if _, ok := task.Find(routable(tasks), "chat"); !ok {
		t.Error("chat should be routable")
	}
}

func TestRouteSchemaOffersEveryTaskID(t *testing.T) {
	tasks, _ := task.Load()
	schema := routeSchema(routable(tasks))
	for _, r := range routable(tasks) {
		if !strings.Contains(schema, `"`+r.ID+`"`) {
			t.Errorf("schema omits %s: %s", r.ID, schema)
		}
	}
	if !strings.Contains(schema, `"enum"`) {
		t.Error("schema does not constrain the answer to an enum")
	}
}

func TestBindRequest(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "meeting.m4a")
	os.WriteFile(audio, []byte("x"), 0o644)
	img := filepath.Join(dir, "photo.png")
	os.WriteFile(img, []byte("x"), 0o644)

	chat := task.Task{ID: "chat", Params: []task.Param{{Name: "prompt", Kind: "text"}}}
	transcribe := task.Task{ID: "transcribe", Params: []task.Param{{Name: "audio", Kind: "file"}}}
	q := "Describe it"
	describe := task.Task{ID: "describe", Params: []task.Param{
		{Name: "image", Kind: "file"}, {Name: "question", Kind: "text", Default: &q},
	}}

	// With no file parameters the request is passed through as written.
	got, err := bindRequest(chat, "what is a mutex")
	if err != nil || len(got) != 1 || got[0] != "what is a mutex" {
		t.Errorf("chat: %q %v", got, err)
	}
	// A named file fills the file parameter; the rest is dropped when the
	// task has nowhere to put it.
	got, err = bindRequest(transcribe, "transcribe "+audio+" please")
	if err != nil || len(got) != 1 || got[0] != audio {
		t.Errorf("transcribe: %q %v", got, err)
	}
	// File first, remaining words second.
	got, err = bindRequest(describe, "what is on the sign in "+img)
	if err != nil || len(got) != 2 || got[0] != img || got[1] != "what is on the sign in" {
		t.Errorf("describe: %q %v", got, err)
	}
	// A task needing a file it cannot find says so rather than guessing.
	if _, err := bindRequest(transcribe, "transcribe the meeting"); err == nil {
		t.Error("missing file accepted")
	}
	// An input takes a named file, else the words of the request.
	summarize := task.Task{ID: "summarize", Params: []task.Param{{Name: "input", Kind: "input"}}}
	got, err = bindRequest(summarize, "summarise "+audio)
	if err != nil || len(got) != 1 || got[0] != audio {
		t.Errorf("input from a file: %q %v", got, err)
	}
	got, err = bindRequest(summarize, "the quick brown fox")
	if err != nil || len(got) != 1 || got[0] != "the quick brown fox" {
		t.Errorf("input from words: %q %v", got, err)
	}
	// Punctuation around a path does not hide it.
	got, err = bindRequest(transcribe, `please transcribe "`+audio+`".`)
	if err != nil || len(got) != 1 || got[0] != audio {
		t.Errorf("quoted path: %q %v", got, err)
	}
}

// chdir moves the process into dir for the test and restores the original
// working directory afterwards, since filesystem search walks relative
// paths from wherever the process happens to be running.
func chdir(t *testing.T, dir string) {
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
}

func TestResolveMissingFileFindsAFileInASubdirectory(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "examples"), 0o755)
	os.WriteFile(filepath.Join(dir, "examples", "invoice.txt"), []byte("Total: 42 EUR"), 0o644)
	chdir(t, dir)

	var errb bytes.Buffer
	a := &app{err: &errb, ue: ui.New(&errb)}

	// --yes uses the single match without asking, the same way it skips
	// confirmChosen's ask.
	got, err := a.resolveMissingFile("what's the value in invoice.txt?", missingWords("what's the value in invoice.txt?"), map[string]string{"--yes": "true"})
	if err != nil {
		t.Fatalf("resolveMissingFile: %v", err)
	}
	want := "what's the value in " + filepath.Join("examples", "invoice.txt") + "?"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// A word with nothing on disk anywhere leaves the request alone rather
	// than erroring: the caller falls back to the original bindRequest error.
	got, err = a.resolveMissingFile("summarise nonexistent.txt", missingWords("summarise nonexistent.txt"), map[string]string{"--yes": "true"})
	if err != nil || got != "" {
		t.Errorf("no match found: %q %v", got, err)
	}
}

func TestResolveMissingFileAsksBeforeUsingAMatch(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "examples"), 0o755)
	os.WriteFile(filepath.Join(dir, "examples", "invoice.txt"), []byte("x"), 0o644)
	chdir(t, dir)

	var errb bytes.Buffer
	a := &app{err: &errb, ue: ui.New(&errb), tty: true, in: strings.NewReader("y\n")}
	got, err := a.resolveMissingFile("read invoice.txt", missingWords("read invoice.txt"), nil)
	if err != nil {
		t.Fatalf("resolveMissingFile: %v", err)
	}
	want := "read " + filepath.Join("examples", "invoice.txt")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !strings.Contains(errb.String(), "invoice.txt") {
		t.Errorf("did not ask about the match: %s", errb.String())
	}

	// Declining leaves the request unresolved.
	a = &app{err: &errb, ue: ui.New(&errb), tty: true, in: strings.NewReader("n\n")}
	got, err = a.resolveMissingFile("read invoice.txt", missingWords("read invoice.txt"), nil)
	if err != nil || got != "" {
		t.Errorf("decline should give up: %q %v", got, err)
	}

	// With no terminal to ask on and no --yes, it fails with the candidate
	// named rather than guessing or hanging on a read.
	a = &app{err: &errb, ue: ui.New(&errb), tty: false}
	if _, err := a.resolveMissingFile("read invoice.txt", missingWords("read invoice.txt"), nil); err == nil {
		t.Error("non-interactive run should refuse to guess")
	}
}

func TestResolveMissingFileListsSeveralMatches(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"a", "b"} {
		os.Mkdir(filepath.Join(dir, sub), 0o755)
		os.WriteFile(filepath.Join(dir, sub, "notes.txt"), []byte("x"), 0o644)
	}
	chdir(t, dir)

	var errb bytes.Buffer
	a := &app{err: &errb, ue: ui.New(&errb), tty: true, in: strings.NewReader("2\n")}
	got, err := a.resolveMissingFile("read notes.txt", missingWords("read notes.txt"), nil)
	if err != nil {
		t.Fatalf("resolveMissingFile: %v", err)
	}
	want := "read " + filepath.Join("b", "notes.txt")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveTaskArgsFindsAnArgumentElsewhere(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0o644)
	chdir(t, dir)

	doc := task.Task{ID: "doc", Params: []task.Param{{Name: "file", Kind: "file"}}}
	var errb bytes.Buffer
	a := &app{err: &errb, ue: ui.New(&errb)}

	got, err := a.resolveTaskArgs(doc, []string{"main.go"}, map[string]string{"--yes": "true"})
	if err != nil {
		t.Fatalf("resolveTaskArgs: %v", err)
	}
	want := []string{filepath.Join("src", "main.go")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// A path that already stats is left alone, and "-" and {} are never
	// treated as filenames to search for.
	transcribe := task.Task{ID: "transcribe", Params: []task.Param{{Name: "audio", Kind: "file"}}}
	got, err = a.resolveTaskArgs(transcribe, []string{filepath.Join("src", "main.go")}, nil)
	if err != nil || !reflect.DeepEqual(got, []string{filepath.Join("src", "main.go")}) {
		t.Errorf("existing path: %v %v", got, err)
	}
	got, err = a.resolveTaskArgs(transcribe, []string{"-"}, nil)
	if err != nil || !reflect.DeepEqual(got, []string{"-"}) {
		t.Errorf("dash placeholder: %v %v", got, err)
	}
	describe := task.Task{ID: "describe", Params: []task.Param{
		{Name: "image", Kind: "file"}, {Name: "question", Kind: "text"},
	}}
	got, err = a.resolveTaskArgs(describe, []string{"{}", "what is this?"}, nil)
	if err != nil || got[0] != "{}" {
		t.Errorf("pipe marker: %v %v", got, err)
	}

	// A word matching nothing anywhere is left as given, so the ordinary
	// "does not exist" error from the task engine still applies.
	got, err = a.resolveTaskArgs(doc, []string{"nosuchfile.go"}, map[string]string{"--yes": "true"})
	if err != nil || !reflect.DeepEqual(got, []string{"nosuchfile.go"}) {
		t.Errorf("no match: %v %v", got, err)
	}
}

func TestRunResolvesAMissingFileArgument(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")

	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0o644)
	chdir(t, dir)

	code, out, errs := e.mote("", "run", "doc", "main.go", "--yes")
	if code != 0 {
		t.Fatalf("run: %d %s", code, errs)
	}
	if !strings.Contains(out, "echo:") {
		t.Errorf("task did not run: %q", out)
	}

	// With no terminal to ask on and no --yes, it refuses to guess.
	if code, _, errs := e.mote("", "run", "doc", "main.go"); code == 0 || !strings.Contains(errs, "main.go") {
		t.Errorf("non-interactive run should refuse to guess: %d %s", code, errs)
	}
}

func TestPipeResolvesAMissingFileArgument(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")

	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0o644)
	chdir(t, dir)

	code, out, errs := e.mote("", "pipe", `doc main.go | chat "again: {}"`, "--yes")
	if code != 0 {
		t.Fatalf("pipe: %d %s", code, errs)
	}
	if !strings.Contains(out, "again: echo:") {
		t.Errorf("second stage did not see the first's output: %q", out)
	}
}

func TestDoRoutesAndRuns(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")

	// The fake model answers a schema with the enum value the prompt
	// mentions, so naming a task in the request picks it.
	code, out, errs := e.mote("", "do", "use chat to explain a mutex")
	if code != 0 {
		t.Fatalf("do: %d %s", code, errs)
	}
	if !strings.Contains(out, "echo:") {
		t.Errorf("task did not run: %q", out)
	}
	if !strings.Contains(errs, "mote run chat") {
		t.Errorf("chosen task not shown: %s", errs)
	}

	// --dry-run stops after choosing.
	code, out, errs = e.mote("", "do", "use chat to explain a mutex", "--dry-run")
	if code != 0 || strings.Contains(out, "echo:") {
		t.Errorf("dry run ran the task: %d %q", code, out)
	}
	if !strings.Contains(errs, "mote run chat") {
		t.Errorf("dry run did not report the choice: %s", errs)
	}

	if code, _, _ := e.mote("", "do"); code != ExitUsage {
		t.Error("empty request should be a usage error")
	}
	if code, _, errs := e.mote("", "do", "anything", "--router", "nope"); code != ExitUsage || !strings.Contains(errs, "nope") {
		t.Errorf("unknown router: %d %s", code, errs)
	}
}

func TestNearestPicksTheClosestDescription(t *testing.T) {
	req := []float32{1, 0, 0}
	ids := []string{"far", "near", "middling"}
	vecs := [][]float32{{0, 1, 0}, {0.9, 0.1, 0}, {0.5, 0.5, 0}}
	id, score := nearest(req, vecs, ids)
	if id != "near" {
		t.Errorf("chose %s (%.3f)", id, score)
	}
	if _, s := nearest(req, [][]float32{{0, 1, 0}}, []string{"far"}); s > 0.001 {
		t.Errorf("orthogonal vectors scored %.3f", s)
	}
	// Mismatched or empty vectors must not panic or win.
	if id, _ := nearest(req, [][]float32{{}, {1, 0, 0}}, []string{"empty", "same"}); id != "same" {
		t.Errorf("chose %s over an identical vector", id)
	}
}

func TestDescribeTaskMentionsTheTask(t *testing.T) {
	tasks, _ := task.Load()
	tr, _ := task.Find(tasks, "transcribe")
	d := describeTask(tr)
	for _, want := range []string{"transcribe", "audio", "text"} {
		if !strings.Contains(strings.ToLower(d), want) {
			t.Errorf("description %q omits %q", d, want)
		}
	}
}

func TestDoWithTheEmbedRouter(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	e.install("bge-small-en-1.5")

	// The fake embeddings are bags of words, so a request that repeats one
	// of chat's own examples lands on chat.
	code, out, errs := e.mote("", "do", "--router", "embed", "--dry-run",
		"explain what a mutex is")
	if code != 0 {
		t.Fatalf("embed router: %d %s", code, errs)
	}
	if !strings.Contains(errs, "embed router chose") {
		t.Errorf("router did not report its choice: %s", errs)
	}
	if !strings.Contains(errs, "mote run chat") {
		t.Errorf("expected chat, got: %s %s", errs, out)
	}

	// The config key selects the router too.
	if code, _, errs := e.mote("", "config", "set", "router", "embed"); code != 0 {
		t.Fatalf("config set router: %s", errs)
	}
	if code, _, errs := e.mote("", "do", "--dry-run", "explain what a mutex is"); code != 0 || !strings.Contains(errs, "embed router chose") {
		t.Errorf("config router not used: %d %s", code, errs)
	}
	if code, _, errs := e.mote("", "config", "set", "router", "nonsense"); code == 0 || !strings.Contains(errs, "text or embed") {
		t.Errorf("bad router accepted: %d %s", code, errs)
	}
}

func TestPassagesKeepExamplesSeparate(t *testing.T) {
	tasks, _ := task.Load()
	chat, _ := task.Find(tasks, "chat")
	if len(chat.Examples) == 0 {
		t.Fatal("chat has no examples to route on")
	}
	texts, owners := passages([]task.Task{chat})
	if len(texts) != len(chat.Examples)+1 {
		t.Fatalf("got %d passages for a task with %d examples", len(texts), len(chat.Examples))
	}
	if len(owners) != len(texts) {
		t.Fatalf("%d owners for %d passages", len(owners), len(texts))
	}
	for i, o := range owners {
		if o != "chat" {
			t.Errorf("passage %d belongs to %q", i, o)
		}
	}
	// An example must stand alone, not be folded into the description.
	for _, e := range chat.Examples {
		found := false
		for _, txt := range texts {
			if txt == e {
				found = true
			}
		}
		if !found {
			t.Errorf("example %q is not its own passage", e)
		}
	}
}

func TestCatalogueShowsExamplesToTheTextRouter(t *testing.T) {
	tasks, _ := task.Load()
	c := catalogue(routable(tasks))
	chat, _ := task.Find(tasks, "chat")
	if !strings.Contains(c, chat.Examples[0]) {
		t.Errorf("catalogue omits chat's examples:\n%s", c)
	}
}
