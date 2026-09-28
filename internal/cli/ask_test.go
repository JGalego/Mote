package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// notes writes a folder of notes and indexes it.
func indexedNotes(t *testing.T, e *env) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "budget.md"), []byte("# Budget\n\nThe committee approved a travel budget of 4000 euros.\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "eng"), 0o755)
	os.WriteFile(filepath.Join(dir, "eng", "retries.txt"), []byte("Retries back off exponentially, up to five attempts.\n"), 0o644)
	code, out, errs := e.mote("", "index", dir)
	if code != 0 || !strings.Contains(out, "2 files: 2 new") {
		t.Fatalf("index: %d %s %s", code, out, errs)
	}
	return dir
}

func askEnv(t *testing.T) *env {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	e.install("bge-small-en-1.5")
	return e
}

func TestIndexAndAsk(t *testing.T) {
	e := askEnv(t)
	if code, _, errs := e.mote("", "ask", "anything"); code != ExitMissing || !strings.Contains(errs, "mote index DIR") {
		t.Errorf("ask before indexing: %d %s", code, errs)
	}
	dir := indexedNotes(t, e)
	budget := filepath.Join(dir, "budget.md")

	code, out, errs := e.mote("", "ask", "what travel budget did the committee approve")
	if code != 0 {
		t.Fatalf("ask: %d %s", code, errs)
	}
	// The model sees the closest passage first, numbered, with its source.
	if !strings.Contains(out, "[1] "+budget+", line 1\n# Budget") || !strings.Contains(out, "Question: what travel budget") {
		t.Errorf("prompt: %s", out)
	}
	if !strings.Contains(errs, "[1] "+budget+":1") {
		t.Errorf("sources: %s", errs)
	}
	// --sources shows the passages without asking a model; --in limits them.
	_, out, _ = e.mote("", "ask", "attempts", "--sources", "--in", filepath.Join(dir, "eng"))
	if !strings.Contains(out, "retries.txt:1") || strings.Contains(out, "budget.md") {
		t.Errorf("sources within eng: %s", out)
	}

	// A second index run reads nothing again.
	if _, out, _ := e.mote("", "index"); !strings.Contains(out, "0 new, 0 changed, 0 removed") {
		t.Errorf("refresh: %s", out)
	}
	if _, out, _ := e.mote("", "index", "status"); !strings.Contains(out, dir) || !strings.Contains(out, "2 files, 2 passages") {
		t.Errorf("status: %s", out)
	}
	if code, out, _ := e.mote("", "index", "rm", dir); code != 0 || !strings.Contains(out, "2 files") {
		t.Errorf("rm: %d %s", code, out)
	}
	if _, out, _ := e.mote("", "index", "status"); !strings.Contains(out, "nothing indexed") {
		t.Errorf("status after rm: %s", out)
	}
	if code, _, _ := e.mote("", "index"); code != ExitUsage {
		t.Errorf("index with nothing to refresh: exit %d", code)
	}
}

func TestAgentSearchesIndexedFiles(t *testing.T) {
	e := askEnv(t)
	indexedNotes(t, e)
	script(t,
		`{"thought":"look in the notes","action":{"tool":"files","args":{"query":"travel budget"}}}`,
		`{"thought":"found it","action":{"tool":"finish","args":{"answer":"4000 euros"}}}`)
	code, out, errs := e.mote("", "agent", "what is the travel budget?")
	if code != 0 || out != "4000 euros\n" {
		t.Fatalf("agent: %d %q %s", code, out, errs)
	}
	// The agent shows the first line of what a tool returned.
	if !strings.Contains(errs, "-> files") || !strings.Contains(errs, "budget.md, line 1") {
		t.Errorf("the files tool did not return the passage:\n%s", errs)
	}
}
