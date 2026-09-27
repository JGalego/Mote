package task

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	mrt "github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/registry"
)

// scripts writes stand-in command-line tools and returns a Tool function
// that finds them, so op error paths can be driven from a test.
func scripts(t *testing.T, bodies map[string]string) func(string) (string, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in tools are shell scripts")
	}
	dir := t.TempDir()
	for name, body := range bodies {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return func(name string) (string, error) {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err != nil {
			return "", errors.New(name + " not available")
		}
		return p, nil
	}
}

func runTask(t *testing.T, id string, e Env, args ...string) (Result, error) {
	t.Helper()
	return mustTask(t, id).Run(context.Background(), e, args, Options{})
}

func TestReadTextLimits(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.txt")
	os.WriteFile(big, make([]byte, maxRead+1), 0o644)
	if _, err := readText(big); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("oversized file: %v", err)
	}
	bin := filepath.Join(dir, "b.bin")
	os.WriteFile(bin, []byte{'a', 0, 'b'}, 0o644)
	if _, err := readText(bin); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Errorf("binary file: %v", err)
	}
	if _, err := readText(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestGenerateInputChecks(t *testing.T) {
	fb := &fakeBackend{reply: func(mrt.Request) string { return "ok" }}
	e := env(fb)

	// describe needs a real image; an empty variable is an error, not an
	// empty prompt.
	dir := t.TempDir()
	img := filepath.Join(dir, "x.png")
	os.WriteFile(img, []byte("png"), 0o644)
	if _, err := runTask(t, "describe", e, img, "what is this"); err != nil {
		t.Fatalf("describe: %v", err)
	}
}

func TestSpeakNeedsSomethingToSay(t *testing.T) {
	fb := &fakeBackend{reply: func(mrt.Request) string { return "ok" }}
	e := env(fb)
	if _, err := runTask(t, "speak", e, "   "); err == nil || !strings.Contains(err.Error(), "nothing to say") {
		t.Errorf("empty speech: %v", err)
	}
}

func TestToolFailuresAreReported(t *testing.T) {
	fb := &fakeBackend{reply: func(mrt.Request) string { return "ok" }}
	dir := t.TempDir()
	vid := filepath.Join(dir, "v.mp4")
	os.WriteFile(vid, []byte("video"), 0o644)

	// A task needing a tool that is not there says which one.
	e := env(fb)
	e.Tool = func(name string) (string, error) { return "", errors.New(name + " is missing") }
	if _, err := runTask(t, "frames", e, vid, "2"); err == nil || !strings.Contains(err.Error(), "ffprobe") {
		t.Errorf("missing ffprobe: %v", err)
	}

	// A tool that fails carries its own output into the error.
	e2 := env(fb)
	e2.Tool = scripts(t, map[string]string{
		"ffprobe": "echo 'broken file' >&2; exit 2",
		"ffmpeg":  "exit 0",
	})
	_, err := runTask(t, "frames", e2, vid, "2")
	if err == nil || !strings.Contains(err.Error(), "broken file") {
		t.Errorf("failing ffprobe: %v", err)
	}

	// A duration that is not a number is caught before ffmpeg runs.
	e3 := env(fb)
	e3.Tool = scripts(t, map[string]string{
		"ffprobe": "echo not-a-number",
		"ffmpeg":  "exit 0",
	})
	_, err = runTask(t, "frames", e3, vid, "2")
	if err == nil || !strings.Contains(err.Error(), "duration") {
		t.Errorf("bad duration: %v", err)
	}

	// ffmpeg that writes no frames is an error, not an empty answer.
	e4 := env(fb)
	e4.Tool = scripts(t, map[string]string{
		"ffprobe": "echo 10",
		"ffmpeg":  "exit 0",
	})
	_, err = runTask(t, "frames", e4, vid, "2")
	if err == nil || !strings.Contains(err.Error(), "no frames") {
		t.Errorf("no frames written: %v", err)
	}
}

func TestFrameCountIsChecked(t *testing.T) {
	fb := &fakeBackend{reply: func(mrt.Request) string { return "ok" }}
	dir := t.TempDir()
	vid := filepath.Join(dir, "v.mp4")
	os.WriteFile(vid, []byte("video"), 0o644)
	e := env(fb)
	e.Tool = scripts(t, map[string]string{"ffprobe": "echo 10", "ffmpeg": "exit 0"})
	for _, n := range []string{"0", "1001", "many"} {
		if _, err := runTask(t, "frames", e, vid, n); err == nil || !errors.Is(err, ErrUsage) {
			t.Errorf("frame count %q: %v", n, err)
		}
	}
}

func TestConvertWidthIsChecked(t *testing.T) {
	fb := &fakeBackend{reply: func(mrt.Request) string { return "ok" }}
	dir := t.TempDir()
	img := filepath.Join(dir, "a.png")
	os.WriteFile(img, []byte("png"), 0o644)
	e := env(fb)
	e.Tool = scripts(t, map[string]string{"ffmpeg": "exit 0"})
	_, err := mustTask(t, "convert").Run(context.Background(), e,
		[]string{img, "-40"}, Options{Output: filepath.Join(dir, "out.png")})
	if err == nil || !strings.Contains(err.Error(), "width") {
		t.Errorf("negative width: %v", err)
	}
}

func TestAudioNeedsAStream(t *testing.T) {
	fb := &fakeBackend{reply: func(mrt.Request) string { return "text" }}
	dir := t.TempDir()
	vid := filepath.Join(dir, "v.mp4")
	os.WriteFile(vid, []byte("video"), 0o644)
	e := env(fb)
	// ffmpeg "succeeds" but writes only a header, which means no audio.
	// Write to the last argument, which is ffmpeg's output path.
	e.Tool = scripts(t, map[string]string{"ffmpeg": "for last; do :; done\nprintf 'RIFF' > \"$last\"\n"})
	if _, err := runTask(t, "transcribe", e, vid); err == nil || !strings.Contains(err.Error(), "no audio stream") {
		t.Errorf("empty audio: %v", err)
	}
}

func TestPatchNeedsTheModelToNameFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644)
	fb := &fakeBackend{reply: func(mrt.Request) string { return "I would rather not" }}
	e := env(fb)
	e.Tool = func(string) (string, error) { return "", errors.New("no git") }
	_, err := runTask(t, "patch", e, dir, "change it")
	if err == nil || !strings.Contains(err.Error(), "=== path ===") {
		t.Errorf("reply without file blocks: %v", err)
	}
}

func TestCollectSkipsWhatItCannotRead(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "good.txt"), []byte("hello\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0, 1, 2}, 0o644)
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("ignored\n"), 0o644)
	big := filepath.Join(dir, "big.txt")
	os.WriteFile(big, make([]byte, maxRead+1), 0o644)

	var prompt string
	fb := &fakeBackend{reply: func(req mrt.Request) string {
		prompt = req.Prompt
		return "=== good.txt ===\nhello, world\n"
	}}
	e := env(fb)
	e.Tool = func(string) (string, error) { return "", errors.New("no git") }
	if _, err := runTask(t, "patch", e, dir, "greet"); err != nil {
		t.Fatalf("patch: %v", err)
	}
	if !strings.Contains(prompt, "good.txt") {
		t.Errorf("readable file missing from the prompt: %s", prompt)
	}
	for _, skipped := range []string{"blob.bin", ".git", "big.txt"} {
		if strings.Contains(prompt, skipped) {
			t.Errorf("%s should have been skipped:\n%s", skipped, prompt)
		}
	}
}

func TestCollectOnAnEmptyDirectory(t *testing.T) {
	fb := &fakeBackend{reply: func(mrt.Request) string { return "" }}
	e := env(fb)
	e.Tool = func(string) (string, error) { return "", errors.New("no git") }
	_, err := runTask(t, "patch", e, t.TempDir(), "change something")
	if err == nil || !strings.Contains(err.Error(), "no readable text files") {
		t.Errorf("empty directory: %v", err)
	}
}

func TestStreamFilterReportsTheFenceLanguage(t *testing.T) {
	var got []string
	var lang string
	f := &streamFilter{
		fences: true,
		emit:   func(s string) { got = append(got, s) },
		lang:   func(l string) { lang = l },
	}
	for _, tok := range []string{"```pyt", "hon\n", "x = 1\n", "y = 2", "\n```"} {
		f.write(tok)
	}
	f.flush()
	if lang != "python" {
		t.Errorf("fence language: %q", lang)
	}
	if body := strings.Join(got, ""); body != "x = 1\ny = 2\n" {
		t.Errorf("body: %q", body)
	}
}

func TestStreamFilterFlushesAPartialLine(t *testing.T) {
	var got []string
	f := &streamFilter{fences: true, emit: func(s string) { got = append(got, s) }}
	f.write("no newline at the end")
	if len(got) != 0 {
		t.Errorf("partial line emitted early: %q", got)
	}
	f.flush()
	if strings.Join(got, "") != "no newline at the end" {
		t.Errorf("flush: %q", got)
	}
}

func TestLastLines(t *testing.T) {
	if got := lastLines("a\nb\nc\nd", 2); got != "c\nd" {
		t.Errorf("lastLines: %q", got)
	}
	if got := lastLines("only", 5); got != "only" {
		t.Errorf("short input: %q", got)
	}
}

func TestSchemaMustBeValidJSON(t *testing.T) {
	fb := &fakeBackend{reply: func(mrt.Request) string { return `{"ok":true}` }}
	e := env(fb)
	dir := t.TempDir()
	doc := filepath.Join(dir, "d.txt")
	os.WriteFile(doc, []byte("hello"), 0o644)
	schema := filepath.Join(dir, "s.json")
	os.WriteFile(schema, []byte("{not json"), 0o644)
	if _, err := runTask(t, "extract", e, doc, schema); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Errorf("invalid schema: %v", err)
	}
}

func TestResolveAndBackendFailures(t *testing.T) {
	fb := &fakeBackend{reply: func(mrt.Request) string { return "ok" }}
	e := env(fb, "text")
	if _, err := runTask(t, "chat", e, "hello"); err == nil {
		t.Error("a capability with no model was accepted")
	}
	e2 := env(fb)
	e2.Backend = func(*registry.Model) (mrt.Backend, error) { return nil, errors.New("no backend") }
	if _, err := runTask(t, "chat", e2, "hello"); err == nil || !strings.Contains(err.Error(), "no backend") {
		t.Errorf("backend failure: %v", err)
	}
}
