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
	return runtime.Result{Text: s.b.reply(req), OutputTokens: 3}, nil
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
	for c := range registry.Capabilities {
		if !caps[c] {
			t.Errorf("no task uses capability %s", c)
		}
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
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0, 1}, 0o644)
	diff := "```diff\n--- a/hello.txt\n+++ b/hello.txt\n@@ -1 +1 @@\n-hello\n+hello, world\n```"
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
	// Now the same diff no longer applies.
	if _, err := mustTask(t, "patch").Run(context.Background(), env(b), []string{dir, "x"}, Options{}); err == nil || !strings.Contains(err.Error(), "does not apply") {
		t.Errorf("stale diff: %v", err)
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
