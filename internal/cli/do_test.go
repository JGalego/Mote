package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgalego/mote/internal/task"
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
	// Punctuation around a path does not hide it.
	got, err = bindRequest(transcribe, `please transcribe "`+audio+`".`)
	if err != nil || len(got) != 1 || got[0] != audio {
		t.Errorf("quoted path: %q %v", got, err)
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
