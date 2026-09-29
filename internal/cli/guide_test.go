package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGuideSystemDescribesMoteItself(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	log := filepath.Join(t.TempDir(), "requests.jsonl")
	t.Setenv("MOTE_FAKE_LOG", log)
	in := "how do I turn a photo into text\n/exit\n"
	code, _, errs := e.mote(in, "guide")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	reqs := fakeLog(t, log)
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	sys, _ := reqs[0]["system"].(string)
	for _, want := range []string{"mote run TASK", "mote pipe", "- code: text -> code", "usage: mote run code"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing %q: %q", want, sys)
		}
	}
}

func TestGuideKeepsTheConversation(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	log := filepath.Join(t.TempDir(), "requests.jsonl")
	t.Setenv("MOTE_FAKE_LOG", log)
	in := "what can you do\ncan I pipe describe into chat\n/new\nagain\n/exit\n"
	code, out, errs := e.mote(in, "guide")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	want := "echo: what can you do\necho: can I pipe describe into chat\necho: again\n"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
	reqs := fakeLog(t, log)
	if len(reqs) != 3 {
		t.Fatalf("%d requests", len(reqs))
	}
	// system + question, then system + one exchange + question, then after
	// /new system + question again.
	for i, n := range []float64{2, 4, 2} {
		if reqs[i]["messages"] != n {
			t.Errorf("request %d had %v messages, want %v", i+1, reqs[i]["messages"], n)
		}
	}
	if code, _, _ := e.mote("", "guide", "stray"); code != ExitUsage {
		t.Errorf("positional argument: exit %d", code)
	}
}
