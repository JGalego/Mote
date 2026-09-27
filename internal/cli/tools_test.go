package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// fakeTools puts stand-ins for ffmpeg, ffprobe and git on PATH, so tasks
// that shell out can run in tests. They are shell scripts using builtins
// only, because these tests run with an otherwise empty PATH.
func fakeTools(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake tools are shell scripts")
	}
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// ffmpeg writes its output argument, which is always last. A frame
	// pattern (frame_%03d.png) becomes the files the caller globs for.
	write("ffmpeg", `#!/bin/sh
for last; do :; done
case "$last" in
  *frame_%03d.png)
    dir=${last%/*}
    for i in 001 002 003; do printf 'PNG' > "$dir/frame_$i.png"; done ;;
  *)
    # More than a bare WAV header, or mote reports an empty stream.
    { printf 'RIFF'; i=0; while [ $i -lt 80 ]; do printf 'x'; i=$((i+1)); done; } > "$last" ;;
esac
exit 0
`)
	write("ffprobe", `#!/bin/sh
echo 12.5
`)
	// git is only used to list a directory's files; an empty answer makes
	// the caller fall back to walking the directory itself.
	write("git", `#!/bin/sh
exit 1
`)
	t.Setenv("PATH", dir)
	return dir
}

func TestTasksThatShellOut(t *testing.T) {
	e := newEnv(t)
	e.setup()
	fakeTools(t)
	e.install("qwen3.5-0.8b")
	dir := t.TempDir()
	vid := filepath.Join(dir, "clip.mp4")
	os.WriteFile(vid, []byte("video"), 0o644)

	// frames: ffprobe gives a duration, ffmpeg writes the stills.
	code, out, errs := e.mote("", "run", "frames", vid, "3", "-o", filepath.Join(dir, "stills"))
	if code != 0 {
		t.Fatalf("frames: %d %s %s", code, out, errs)
	}
	if !strings.Contains(out, "frame_001.png") {
		t.Errorf("frames listed: %q", out)
	}
	if code, _, errs := e.mote("", "run", "frames", vid, "0"); code != ExitUsage || !strings.Contains(errs, "1-1000") {
		t.Errorf("bad frame count: %d %s", code, errs)
	}

	// convert: ffmpeg writes the output file.
	png := filepath.Join(dir, "small.png")
	if code, _, errs := e.mote("", "run", "convert", vid, "-o", png); code != 0 {
		t.Errorf("convert: %d %s", code, errs)
	}
	if _, err := os.Stat(png); err != nil {
		t.Errorf("convert wrote nothing: %v", err)
	}
	// Refusing to overwrite the input is a usage error, not a lost file.
	if code, _, errs := e.mote("", "run", "convert", vid, "-o", vid); code != ExitUsage || !strings.Contains(errs, "overwrite") {
		t.Errorf("convert onto itself: %d %s", code, errs)
	}

	// transcribe: ffmpeg makes a wav, then the asr model reads it.
	t.Setenv("MOTE_FAKE_ASR", "1")
	e.install("qwen3-asr-0.6b")
	if code, out, errs := e.mote("", "run", "transcribe", vid); code != 0 || !strings.Contains(out, "echo") {
		t.Errorf("transcribe: %d %s %s", code, out, errs)
	}
	// video: frames plus speech, summarised.
	if code, out, errs := e.mote("", "run", "video", vid); code != 0 || out == "" {
		t.Errorf("video: %d %s %s", code, out, errs)
	}
}

func TestListenHearsAWakeWordAndRuns(t *testing.T) {
	e := newEnv(t)
	e.setup()
	fakeTools(t)
	e.install("qwen3.5-0.8b")
	e.install("qwen3-asr-0.6b")
	// The fake asr model echoes what it is given; the transcribe task's
	// reply therefore contains the wake word, which drives one request.
	t.Setenv("MOTE_FAKE_ASR", "1")

	code, out, errs := e.mote("", "listen", "--wake", "echo", "--once", "--chunk", "1")
	if code != 0 {
		t.Fatalf("listen: %d %s %s", code, out, errs)
	}
	if !strings.Contains(errs, "listening for") {
		t.Errorf("listen did not announce itself: %s", errs)
	}
	if out == "" {
		t.Errorf("listen produced no answer: %s", errs)
	}
}

// readOnlyDir returns a directory nothing can be written into, to exercise
// the error paths of code that writes files.
func readOnlyDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("file permissions do not stop writes here")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	return locked
}

func TestWritingOutputThatCannotBeWritten(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	locked := readOnlyDir(t)

	code, _, errs := e.mote("", "run", "chat", "hello", "-o", filepath.Join(locked, "out.txt"))
	if code == 0 {
		t.Errorf("writing into a read-only directory reported success")
	}
	if !strings.Contains(strings.ToLower(errs), "permission") && !strings.Contains(errs, "denied") {
		t.Errorf("unhelpful error: %s", errs)
	}
}

func TestMemoryFailsLoudlyWhenItCannotWrite(t *testing.T) {
	e := newEnv(t)
	e.setup()
	locked := readOnlyDir(t)
	t.Setenv("MOTE_CONFIG_DIR", locked)
	if code, _, errs := e.mote("", "remember", "a fact"); code == 0 {
		t.Errorf("remember into a read-only config dir reported success: %s", errs)
	}
}

func TestRunRejectsMissingAndWrongInputs(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	dir := t.TempDir()

	// The wording of a stat error is the operating system's, so check that
	// mote names the file and calls it a usage problem.
	if code, _, errs := e.mote("", "run", "doc", filepath.Join(dir, "nope.go")); code != ExitUsage || !strings.Contains(errs, "nope.go") {
		t.Errorf("missing file: %d %s", code, errs)
	}
	// A directory where a file is expected, and the reverse.
	if code, _, errs := e.mote("", "run", "doc", dir); code != ExitUsage || !strings.Contains(errs, "not a file") {
		t.Errorf("directory as a file: %d %s", code, errs)
	}
	f := filepath.Join(dir, "a.txt")
	os.WriteFile(f, []byte("x"), 0o644)
	if code, _, errs := e.mote("", "run", "patch", f, "do something"); code != ExitUsage || !strings.Contains(errs, "not a dir") {
		t.Errorf("file as a directory: %d %s", code, errs)
	}
	// Too many arguments is caught before any model runs.
	if code, _, errs := e.mote("", "run", "chat", "one", "two"); code != ExitUsage || !strings.Contains(errs, "too many arguments") {
		t.Errorf("extra argument: %d %s", code, errs)
	}
}

func TestPatchOnADirectoryWithNothingToRead(t *testing.T) {
	e := newEnv(t)
	e.setup()
	fakeTools(t)
	e.install("qwen3.5-0.8b")
	// A directory holding only binary files has no text to patch.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0, 1, 2, 0, 3}, 0o644)
	code, _, errs := e.mote("", "run", "patch", dir, "change something")
	if code == 0 || !strings.Contains(errs, "no readable text files") {
		t.Errorf("binary-only directory: %d %s", code, errs)
	}
}

func TestPipeStageFailuresAreReported(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	// The second stage needs a file the request cannot supply.
	code, _, errs := e.mote("", "pipe", "chat hello | describe")
	if code == 0 || !strings.Contains(errs, "describe") {
		t.Errorf("stage that cannot bind: %d %s", code, errs)
	}
}

// A local program wrapped as a task runs like any other: from `mote run`,
// inside a pipeline, and listed with the program it needs.
func TestExecTasksWrapLocalPrograms(t *testing.T) {
	e := newEnv(t)
	e.setup()
	bin := fakeTools(t)
	e.install("qwen3.5-0.8b")
	// wc stands in for a real tool: it prints its arguments, one per line,
	// then counts the lines it was given on stdin.
	os.WriteFile(filepath.Join(bin, "wc"), []byte(`#!/bin/sh
for a; do echo "arg:$a"; done
n=0
while IFS= read -r line; do n=$((n+1)); done
echo "lines:$n"
`), 0o755)
	dir := t.TempDir()
	t.Setenv("MOTE_TASKS_DIR", dir)
	os.WriteFile(filepath.Join(dir, "tools.json"), []byte(`{"tasks":[
	  {"id":"count","summary":"Count lines","in":["text"],"out":"text",
	   "params":[{"name":"text","kind":"text"},{"name":"label","kind":"text","optional":true}],
	   "steps":[{"op":"exec","cmd":["wc","-l","--","{{label}}"],"from":"text","as":"out"}]},
	  {"id":"absent","summary":"Needs a program that is not here","in":["text"],"out":"text",
	   "params":[{"name":"text","kind":"text"}],
	   "steps":[{"op":"exec","cmd":["nonesuch","{{text}}"],"as":"out"}]}]}`), 0o644)

	code, out, errs := e.mote("", "run", "count", "a\nb\nc", "my label")
	if code != 0 {
		t.Fatalf("run: %d %s", code, errs)
	}
	for _, want := range []string{"arg:-l", "arg:--", "arg:my label", "lines:2"} {
		if !strings.Contains(out, want) {
			t.Errorf("run output lacks %q: %q", want, out)
		}
	}

	// In a pipeline the model's reply is the program's input.
	code, out, errs = e.mote("", "pipe", "chat 'one' | count")
	if code != 0 || !strings.Contains(out, "arg:-l") || strings.Contains(out, "arg:my label") {
		t.Fatalf("pipe: %d %q %s", code, out, errs)
	}

	code, out, _ = e.mote("", "tasks")
	if code != 0 || !regexp.MustCompile(`wc: \S*ok`).MatchString(out) || !regexp.MustCompile(`nonesuch: \S*missing`).MatchString(out) {
		t.Errorf("tasks should report the wrapped programs: %s", out)
	}
	if code, _, errs := e.mote("", "run", "absent", "x"); code == 0 || !strings.Contains(errs, "nonesuch") {
		t.Errorf("missing program: %d %s", code, errs)
	}
}
