package task

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/registry"
)

type fakeBackend struct {
	reply   func(req runtime.Request) string
	opened  int
	prompts []runtime.Request
}

type fakeSession struct{ b *fakeBackend }

func (b *fakeBackend) Open(context.Context, *registry.Model, map[string]string) (runtime.Session, error) {
	b.opened++
	return fakeSession{b}, nil
}

func (b *fakeBackend) Speak(_ context.Context, _ *registry.Model, _ map[string]string, text, out string) (runtime.Stats, error) {
	return runtime.Stats{}, os.WriteFile(out, []byte("RIFF"+text), 0o644)
}

func (s fakeSession) Generate(_ context.Context, req runtime.Request) (runtime.Result, error) {
	s.b.prompts = append(s.b.prompts, req)
	text := s.b.reply(req)
	if req.OnToken != nil {
		req.OnToken(text)
	}
	return runtime.Result{Text: text, OutputTokens: 3}, nil
}
func (fakeSession) Close() runtime.Stats { return runtime.Stats{} }

func env(b *fakeBackend, missing ...string) Env {
	return Env{
		Resolve: func(_ context.Context, c string) (*registry.Model, map[string]string, error) {
			for _, m := range missing {
				if m == c {
					return nil, nil, errors.New("no model for " + c)
				}
			}
			return &registry.Model{ID: "m-" + c}, nil, nil
		},
		Backend: func(*registry.Model) (runtime.Backend, error) { return b, nil },
		Tool: func(n string) (string, error) {
			return exec.LookPath(n)
		},
	}
}

func mustTask(t *testing.T, id string) Task {
	tasks, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	tk, ok := Find(tasks, id)
	if !ok {
		t.Fatalf("task %s missing", id)
	}
	return tk
}

func TestBuiltinTasksValid(t *testing.T) {
	tasks, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) < 10 {
		t.Errorf("only %d tasks", len(tasks))
	}
	caps := map[string]bool{}
	for _, tk := range tasks {
		for _, c := range tk.Caps() {
			caps[c] = true
		}
	}
	// Every capability must have a user. Most are used by a task; these
	// are used by a command instead, which tasks.json cannot express.
	byCommand := map[string]string{"embed": "mote do --router embed"}
	for c := range registry.Capabilities {
		if !caps[c] && byCommand[c] == "" {
			t.Errorf("no task uses capability %s", c)
		}
	}
	if got := mustTask(t, "patch").Tools(); len(got) != 0 {
		t.Errorf("patch should not need tools: %v", got)
	}
	if got := mustTask(t, "video").Tools(); strings.Join(got, ",") != "ffmpeg,ffprobe" {
		t.Errorf("video tools %v", got)
	}
}

func TestParseRejects(t *testing.T) {
	bad := map[string]string{
		"unknown op":  `{"tasks":[{"id":"x","steps":[{"op":"teleport","as":"out"}]}]}`,
		"unknown cap": `{"tasks":[{"id":"x","steps":[{"op":"generate","cap":"smell","as":"out"}]}]}`,
		"undefined":   `{"tasks":[{"id":"x","steps":[{"op":"read","from":"nope","as":"out"}]}]}`,
		"template":    `{"tasks":[{"id":"x","steps":[{"op":"generate","cap":"text","prompt":"{{ghost}}","as":"out"}]}]}`,
		"no out":      `{"tasks":[{"id":"x","steps":[{"op":"generate","cap":"text","as":"y"}]}]}`,
		"dup":         `{"tasks":[{"id":"x","steps":[{"op":"generate","cap":"text","as":"out"}]},{"id":"x","steps":[{"op":"generate","cap":"text","as":"out"}]}]}`,
		"param kind":  `{"tasks":[{"id":"x","params":[{"name":"a","kind":"blob"}],"steps":[{"op":"generate","cap":"text","as":"out"}]}]}`,
		"extra field": `{"tasks":[{"id":"x","shell":"rm -rf /","steps":[{"op":"generate","cap":"text","as":"out"}]}]}`,
	}
	for name, doc := range bad {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRunChat(t *testing.T) {
	b := &fakeBackend{reply: func(r runtime.Request) string { return "hi: " + r.Prompt }}
	res, err := mustTask(t, "chat").Run(context.Background(), env(b), []string{"hello"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "hi: hello" || len(res.Calls) != 1 || res.Calls[0].Model != "m-text" {
		t.Errorf("got %+v", res)
	}
}

func TestRunStdinAndUsageErrors(t *testing.T) {
	b := &fakeBackend{reply: func(r runtime.Request) string { return r.Prompt }}
	e := env(b)
	e.Stdin = strings.NewReader("from stdin")
	res, err := mustTask(t, "chat").Run(context.Background(), e, []string{"-"}, Options{})
	if err != nil || res.Text != "from stdin" {
		t.Errorf("stdin: %q %v", res.Text, err)
	}
	_, err = mustTask(t, "chat").Run(context.Background(), e, nil, Options{})
	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "PROMPT") {
		t.Errorf("missing arg: %v", err)
	}
	_, err = mustTask(t, "chat").Run(context.Background(), e, []string{"a", "b"}, Options{})
	if !errors.Is(err, ErrUsage) {
		t.Errorf("extra arg: %v", err)
	}
	_, err = mustTask(t, "describe").Run(context.Background(), e, []string{"/no/such.png"}, Options{})
	if !errors.Is(err, ErrUsage) {
		t.Errorf("missing file: %v", err)
	}
	_, err = mustTask(t, "convert").Run(context.Background(), e, []string{"x"}, Options{})
	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "-o") {
		t.Errorf("required output: %v", err)
	}
}

func TestRunCodeStripsFences(t *testing.T) {
	b := &fakeBackend{reply: func(runtime.Request) string { return "Sure:\n```python\ndef f():\n    return 1\n```\nDone." }}
	res, err := mustTask(t, "code").Run(context.Background(), env(b), []string{"f returns 1"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "def f():\n    return 1\n" {
		t.Errorf("got %q", res.Text)
	}
}

func TestRunExtractJSON(t *testing.T) {
	dir := t.TempDir()
	doc := filepath.Join(dir, "invoice.txt")
	os.WriteFile(doc, []byte("Invoice 42, total 10 EUR"), 0o644)
	schema := filepath.Join(dir, "s.json")
	os.WriteFile(schema, []byte(`{"type":"object","properties":{"total":{"type":"number"}}}`), 0o644)

	b := &fakeBackend{reply: func(runtime.Request) string { return `{"total": 10}` }}
	res, err := mustTask(t, "extract").Run(context.Background(), env(b), []string{doc, schema}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, `"total": 10`) || !strings.Contains(string(b.prompts[0].JSONSchema), "total") {
		t.Errorf("got %q schema %s", res.Text, b.prompts[0].JSONSchema)
	}
	// Without a schema a generic object schema is used.
	res, err = mustTask(t, "extract").Run(context.Background(), env(b), []string{doc}, Options{})
	if err != nil || string(b.prompts[1].JSONSchema) != `{"type":"object"}` {
		t.Errorf("default schema: %v %s", err, b.prompts[1].JSONSchema)
	}
	b.reply = func(runtime.Request) string { return "not json" }
	if _, err := mustTask(t, "extract").Run(context.Background(), env(b), []string{doc}, Options{}); err == nil {
		t.Error("invalid JSON accepted")
	}
	os.WriteFile(doc, []byte{0, 1, 2}, 0o644)
	if _, err := mustTask(t, "extract").Run(context.Background(), env(b), []string{doc}, Options{}); err == nil {
		t.Error("binary input accepted")
	}
}

func TestRunDescribeAndTranscribe(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "a.png")
	wav := filepath.Join(dir, "a.wav")
	os.WriteFile(img, []byte("png"), 0o644)
	os.WriteFile(wav, []byte("wav"), 0o644)
	b := &fakeBackend{reply: func(r runtime.Request) string {
		return strings.Join(append(r.Images, r.Audio...), ",") + "|" + r.Prompt
	}}
	res, err := mustTask(t, "describe").Run(context.Background(), env(b), []string{img}, Options{})
	if err != nil || res.Text != img+"|Describe this image in two sentences." {
		t.Errorf("describe: %q %v", res.Text, err)
	}
	res, err = mustTask(t, "transcribe").Run(context.Background(), env(b), []string{wav}, Options{})
	if err != nil || res.Text != wav+"|" {
		t.Errorf("transcribe: %q %v", res.Text, err)
	}
	_, err = mustTask(t, "transcribe").Run(context.Background(), env(b, "asr"), []string{wav}, Options{})
	if err == nil || !strings.Contains(err.Error(), "no model for asr") {
		t.Errorf("unavailable model: %v", err)
	}
}

func TestRunSpeak(t *testing.T) {
	out := filepath.Join(t.TempDir(), "o.wav")
	b := &fakeBackend{}
	res, err := mustTask(t, "speak").Run(context.Background(), env(b), []string{"hello"}, Options{Output: out})
	if err != nil || res.Files[0] != out {
		t.Fatalf("%+v %v", res, err)
	}
	if got, _ := os.ReadFile(out); string(got) != "RIFFhello" {
		t.Errorf("wav %q", got)
	}
}

func TestSessionReuse(t *testing.T) {
	tk := Task{ID: "two", Params: []Param{{Name: "p", Kind: "text"}}, Steps: []Step{
		{Op: "generate", Cap: "text", Prompt: "{{p}}", As: "a"},
		{Op: "generate", Cap: "text", Prompt: "{{a}}!", As: "out"},
	}}
	if err := tk.validate(); err != nil {
		t.Fatal(err)
	}
	b := &fakeBackend{reply: func(r runtime.Request) string { return r.Prompt + "?" }}
	res, err := tk.Run(context.Background(), env(b), []string{"x"}, Options{})
	if err != nil || res.Text != "x?!?" || b.opened != 1 {
		t.Errorf("%q %v opened=%d", res.Text, err, b.opened)
	}
}

func TestPatch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0, 1}, 0o644)
	diff := "Here you go:\n=== hello.txt ===\n```\nhello, world\n```\n"
	b := &fakeBackend{reply: func(r runtime.Request) string {
		if !strings.Contains(r.Prompt, "=== hello.txt ===") || strings.Contains(r.Prompt, "blob.bin") {
			return "bad context"
		}
		return diff
	}}
	res, err := mustTask(t, "patch").Run(context.Background(), env(b), []string{dir, "greet the world"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Text, "--- a/hello.txt") {
		t.Errorf("diff %q", res.Text)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "hello.txt")); string(got) != "hello\n" {
		t.Error("file changed without --apply")
	}
	if _, err := mustTask(t, "patch").Run(context.Background(), env(b), []string{dir, "x"}, Options{Apply: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "hello.txt")); string(got) != "hello, world\n" {
		t.Errorf("not applied: %q", got)
	}
	// Re-running the same change is detected as a no-op.
	if _, err := mustTask(t, "patch").Run(context.Background(), env(b), []string{dir, "x"}, Options{}); err == nil || !strings.Contains(err.Error(), "no changes") {
		t.Errorf("no-op change: %v", err)
	}
	b.reply = func(runtime.Request) string { return "=== ../outside.txt ===\nx\n" }
	if _, err := mustTask(t, "patch").Run(context.Background(), env(b), []string{dir, "x"}, Options{Apply: true}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Errorf("escape accepted: %v", err)
	}
}

func TestMediaTasks(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	vid := filepath.Join(dir, "v.mp4")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=duration=2:size=64x48:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-shortest", "-pix_fmt", "yuv420p", vid).CombinedOutput(); err != nil {
		t.Fatalf("make video: %v %s", err, out)
	}
	b := &fakeBackend{reply: func(r runtime.Request) string {
		if len(r.Audio) > 0 {
			return "a tone"
		}
		if len(r.Images) > 0 {
			return "test pattern"
		}
		return "summary of: " + r.Prompt
	}}
	frames := filepath.Join(dir, "frames")
	res, err := mustTask(t, "frames").Run(context.Background(), env(b), []string{vid, "3"}, Options{Output: frames})
	if err != nil || len(res.Files) != 3 {
		t.Fatalf("frames: %v %v", res.Files, err)
	}
	res, err = mustTask(t, "video").Run(context.Background(), env(b), []string{vid}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "4. test pattern") || !strings.Contains(res.Text, "a tone") {
		t.Errorf("video summary prompt: %q", res.Text)
	}
	out := filepath.Join(dir, "small.png")
	if _, err := mustTask(t, "convert").Run(context.Background(), env(b), []string{filepath.Join(frames, "frame_001.png"), "32"}, Options{Output: out}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Error("converted image missing")
	}
}

func TestUnified(t *testing.T) {
	before := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n"
	after := "a\nB\nc\nd\ne\nf\ng\nh\ni\nj\nk\n"
	want := "--- a/x\n+++ b/x\n@@ -1,5 +1,5 @@\n a\n-b\n+B\n c\n d\n e\n@@ -8,3 +8,4 @@\n h\n i\n j\n+k\n"
	if got := Unified("x", before, after); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if Unified("x", "same\n", "same\n") != "" {
		t.Error("equal texts produced a diff")
	}
	if got := Unified("n", "", "new\n"); got != "--- a/n\n+++ b/n\n@@ -0,0 +1,1 @@\n+new\n" {
		t.Errorf("new file diff %q", got)
	}
}

func TestParseFiles(t *testing.T) {
	files, order := ParseFiles("intro\n=== a.go ===\n```go\npackage a\n```\n=== b/c.txt ===\nline\n")
	if len(order) != 2 || files["a.go"] != "package a\n" || files["b/c.txt"] != "line\n" {
		t.Errorf("%v %q", order, files)
	}
}

func TestStreamFilter(t *testing.T) {
	var got strings.Builder
	f := &streamFilter{fences: true, emit: func(s string) { got.WriteString(s) }}
	for _, tok := range []string{"\n``", "`py", "thon\ndef f", "():\n  ", "return 1\n```", "\n"} {
		f.write(tok)
	}
	f.flush()
	if got.String() != "def f():\n  return 1\n" {
		t.Errorf("fences: %q", got.String())
	}
	got.Reset()
	f = &streamFilter{after: "<asr_text>", emit: func(s string) { got.WriteString(s) }}
	for _, tok := range []string{"language Eng", "lish<asr", "_text>Hello", " there."} {
		f.write(tok)
	}
	f.flush()
	if got.String() != "Hello there." {
		t.Errorf("marker: %q", got.String())
	}
}

func TestStreamingAllFinalSteps(t *testing.T) {
	b := &fakeBackend{reply: func(r runtime.Request) string { return "```\nx = 1\n```" }}
	e := env(b)
	var streamed strings.Builder
	e.Stream = func(s string) { streamed.WriteString(s) }
	res, err := mustTask(t, "code").Run(context.Background(), e, []string{"x"}, Options{})
	if err != nil || !res.Streamed || res.Text != "x = 1\n" {
		t.Errorf("%+v %v", res, err)
	}
}

const customTask = `{"tasks":[{"id":"shout","summary":"Answer loudly","in":["text"],"out":"text",
  "params":[{"name":"prompt","kind":"text"}],
  "steps":[{"op":"generate","cap":"text","prompt":"{{prompt}} IN CAPITALS","as":"out"}]}]}`

func writeTasks(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFromAddsUserTasks(t *testing.T) {
	dir := t.TempDir()
	writeTasks(t, dir, "shout.json", customTask)

	tasks, err := LoadFrom(dir)
	if err != nil {
		t.Fatal(err)
	}
	built, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != len(built)+1 {
		t.Fatalf("got %d tasks, want %d", len(tasks), len(built)+1)
	}
	shout, ok := Find(tasks, "shout")
	if !ok {
		t.Fatal("custom task not loaded")
	}
	if !shout.Custom() || shout.Source != filepath.Join(dir, "shout.json") {
		t.Errorf("source %q custom %v", shout.Source, shout.Custom())
	}
	if chat, _ := Find(tasks, "chat"); chat.Custom() {
		t.Error("built-in marked as custom")
	}
	// Tasks stay sorted by id, wherever they came from.
	for i := 1; i < len(tasks); i++ {
		if tasks[i-1].ID > tasks[i].ID {
			t.Fatalf("not sorted: %s before %s", tasks[i-1].ID, tasks[i].ID)
		}
	}
}

func TestLoadFromOverridesBuiltIn(t *testing.T) {
	dir := t.TempDir()
	writeTasks(t, dir, "mine.json", strings.Replace(customTask, `"id":"shout"`, `"id":"chat"`, 1))

	tasks, err := LoadFrom(dir)
	if err != nil {
		t.Fatal(err)
	}
	built, _ := Load()
	if len(tasks) != len(built) {
		t.Errorf("override added a task: %d vs %d", len(tasks), len(built))
	}
	chat, _ := Find(tasks, "chat")
	if !chat.Custom() || chat.Summary != "Answer loudly" {
		t.Errorf("built-in not replaced: %+v", chat)
	}
}

func TestLoadFromReportsBadFiles(t *testing.T) {
	dir := t.TempDir()
	writeTasks(t, dir, "broken.json", `{"tasks":[{"id":"x"}]}`)
	_, err := LoadFrom(dir)
	if err == nil || !strings.Contains(err.Error(), "broken.json") {
		t.Fatalf("error should name the file: %v", err)
	}

	dir = t.TempDir()
	writeTasks(t, dir, "a.json", customTask)
	writeTasks(t, dir, "b.json", customTask)
	_, err = LoadFrom(dir)
	if err == nil || !strings.Contains(err.Error(), "already defined") {
		t.Fatalf("duplicate ids across files: %v", err)
	}

	dir = t.TempDir()
	writeTasks(t, dir, "unknown-op.json", strings.Replace(customTask, `"op":"generate"`, `"op":"nosuchop"`, 1))
	if _, err := LoadFrom(dir); err == nil {
		t.Fatal("unknown op accepted")
	}
}

func TestLoadFromMissingDirIsFine(t *testing.T) {
	tasks, err := LoadFrom(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatal(err)
	}
	built, _ := Load()
	if len(tasks) != len(built) {
		t.Errorf("got %d tasks, want %d", len(tasks), len(built))
	}
	if tasks, err := LoadFrom(""); err != nil || len(tasks) != len(built) {
		t.Errorf("empty dir: %d %v", len(tasks), err)
	}
}

func TestMemoryGoesInFrontOfTheSystemPrompt(t *testing.T) {
	b := &fakeBackend{reply: func(runtime.Request) string { return "ok" }}
	e := env(b)
	e.Memory = "Remember these facts about this user and their work:\n- I write Go"
	if _, err := mustTask(t, "chat").Run(context.Background(), e, []string{"hello"}, Options{}); err != nil {
		t.Fatal(err)
	}
	if len(b.prompts) != 1 {
		t.Fatalf("%d generations", len(b.prompts))
	}
	if !strings.Contains(b.prompts[0].System, "I write Go") {
		t.Errorf("memory missing from the system prompt: %q", b.prompts[0].System)
	}

	// A task with its own system prompt keeps it, after the memory.
	b2 := &fakeBackend{reply: func(runtime.Request) string { return "ok" }}
	e2 := env(b2)
	e2.Memory = "REMEMBERED"
	if _, err := mustTask(t, "code").Run(context.Background(), e2, []string{"a function"}, Options{}); err != nil {
		t.Fatal(err)
	}
	sys := b2.prompts[0].System
	if !strings.HasPrefix(sys, "REMEMBERED") || !strings.Contains(sys, "You write correct, minimal code") {
		t.Errorf("memory did not lead the task's own system prompt: %q", sys)
	}

	// Without memory the system prompt is untouched.
	b3 := &fakeBackend{reply: func(runtime.Request) string { return "ok" }}
	if _, err := mustTask(t, "code").Run(context.Background(), env(b3), []string{"a function"}, Options{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b3.prompts[0].System, "REMEMBERED") || strings.HasPrefix(b3.prompts[0].System, "\n") {
		t.Errorf("unexpected system prompt: %q", b3.prompts[0].System)
	}
}
