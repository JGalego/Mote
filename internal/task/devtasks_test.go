package task

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgalego/mote/internal/runtime"
)

// gitRepo makes a repository with one commit and returns its directory.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	os.WriteFile(filepath.Join(dir, "calc.py"), []byte("def add(a, b):\n    return a + b\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "feat: add calc")
	return dir
}

func echoPrompt() *fakeBackend {
	return &fakeBackend{reply: func(req runtime.Request) string { return req.Prompt }}
}

func TestCommitDescribesTheStagedDiff(t *testing.T) {
	dir := gitRepo(t)
	b := echoPrompt()
	if _, err := runTask(t, "commit", env(b), dir); err == nil || !strings.Contains(err.Error(), "nothing is staged") {
		t.Fatalf("with nothing staged: %v", err)
	}
	if len(b.prompts) != 0 {
		t.Error("the model was asked about an empty diff")
	}
	os.WriteFile(filepath.Join(dir, "calc.py"), []byte("def add(a, b):\n    return a - b\n"), 0o644)
	exec.Command("git", "-C", dir, "add", "calc.py").Run()
	res, err := runTask(t, "commit", env(b), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"feat: add calc", "-    return a + b", "+    return a - b"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("prompt lacks %q:\n%s", want, res.Text)
		}
	}
}

func TestReviewSeesUncommittedChanges(t *testing.T) {
	dir := gitRepo(t)
	b := echoPrompt()
	if _, err := runTask(t, "review", env(b), dir); err == nil || !strings.Contains(err.Error(), "no changes") {
		t.Fatalf("a clean tree: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "calc.py"), []byte("def add(a, b):\n    return a * b\n"), 0o644)
	res, err := runTask(t, "review", env(b), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "+    return a * b") || !strings.Contains(b.prompts[0].System, "No problems found") {
		t.Errorf("review prompt: %q", res.Text)
	}
	// A value that looks like an option never reaches git as one.
	if _, err := runTask(t, "review", env(b), dir, "--output=/tmp/x"); err == nil {
		t.Error("an option was passed as the revision")
	}
}

func TestExplainTakesTextOrAFile(t *testing.T) {
	b := echoPrompt()
	res, err := runTask(t, "explain", env(b), "error[E0382]: borrow of moved value")
	if err != nil || !strings.Contains(res.Text, "E0382") {
		t.Errorf("text: %q %v", res.Text, err)
	}
	f := filepath.Join(t.TempDir(), "build.log")
	os.WriteFile(f, []byte("ld: undefined reference to `main'"), 0o644)
	if res, err = runTask(t, "explain", env(b), f); err != nil || !strings.Contains(res.Text, "undefined reference") {
		t.Errorf("file: %q %v", res.Text, err)
	}
}

func TestOCRAsksTheVisionModel(t *testing.T) {
	img := filepath.Join(t.TempDir(), "shot.png")
	os.WriteFile(img, []byte("png"), 0o644)
	b := &fakeBackend{reply: func(req runtime.Request) string { return "HELLO" }}
	res, err := runTask(t, "ocr", env(b), img)
	if err != nil || res.Text != "HELLO" {
		t.Fatalf("%q %v", res.Text, err)
	}
	if len(b.prompts[0].Images) != 1 || b.prompts[0].Prompt != ocrPrompt {
		t.Errorf("request: %+v", b.prompts[0])
	}
}
