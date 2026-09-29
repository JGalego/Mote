package cli

import (
	"slices"
	"strings"
	"testing"
)

func TestCompletionScript(t *testing.T) {
	e := newEnv(t)
	for _, shell := range []string{"bash", "zsh", "fish"} {
		code, out, errs := e.mote("", "completion", shell)
		if code != 0 || out == "" {
			t.Fatalf("completion %s: %d %q %s", shell, code, out, errs)
		}
		if !strings.Contains(out, "mote") {
			t.Errorf("completion %s: script does not mention mote: %s", shell, out)
		}
	}
	if code, _, _ := e.mote("", "completion", "tcsh"); code != ExitUsage {
		t.Errorf("completion tcsh: want usage error, got exit %d", code)
	}
	if code, _, _ := e.mote("", "completion"); code != ExitUsage {
		t.Errorf("completion (no shell): want usage error, got exit %d", code)
	}
}

func TestCompleteTopLevel(t *testing.T) {
	e := newEnv(t)
	_, out, _ := e.mote("", "__complete", "")
	for _, want := range []string{"run", "chat", "serve", "models", "config", "completion"} {
		if !slices.Contains(strings.Fields(out), want) {
			t.Errorf("top-level completion missing %q, got: %s", want, out)
		}
	}
	_, out, _ = e.mote("", "__complete", "ru")
	if got := strings.Fields(out); len(got) != 1 || got[0] != "run" {
		t.Errorf("prefix completion of %q: got %v, want [run]", "ru", got)
	}
}

func TestCompleteSubcommands(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"config", ""}, "set"},
		{[]string{"models", ""}, "pull"},
		{[]string{"serve", ""}, "status"},
		{[]string{"mcp", ""}, "tools"},
		{[]string{"memory", ""}, "search"},
		{[]string{"index", ""}, "rm"},
		{[]string{"completion", ""}, "bash"},
		{[]string{"models", "why", ""}, "text"},
	}
	for _, c := range cases {
		_, out, _ := e.mote("", append([]string{"__complete"}, c.args...)...)
		if !slices.Contains(strings.Fields(out), c.want) {
			t.Errorf("__complete %v: missing %q, got: %s", c.args, c.want, out)
		}
	}
}

func TestCompleteRunTasks(t *testing.T) {
	e := newEnv(t)
	_, out, _ := e.mote("", "__complete", "run", "")
	if !slices.Contains(strings.Fields(out), "chat") {
		t.Errorf("run task completion missing the built-in chat task, got: %s", out)
	}
}

func TestCompleteFlags(t *testing.T) {
	e := newEnv(t)
	_, out, _ := e.mote("", "__complete", "run", "chat", "--a")
	got := strings.Fields(out)
	if len(got) != 1 || got[0] != "--apply" {
		t.Errorf("flag completion of --a for run: got %v, want [--apply]", got)
	}
	// A bare word (not starting with '-') after the task falls through to
	// no candidates, so the shell's own file completion takes over.
	_, out, _ = e.mote("", "__complete", "run", "chat", "some")
	if out != "" {
		t.Errorf("expected no candidates for a non-flag run argument, got: %s", out)
	}
}
