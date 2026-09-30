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

func TestCompletionOffersNbAndKernelSubcommandsAndTheirOwnFlags(t *testing.T) {
	e := newEnv(t)
	complete := func(args ...string) []string {
		_, out, _ := e.mote("", append([]string{"__complete"}, args...)...)
		return strings.Fields(out)
	}
	if got := strings.Join(complete("nb", ""), " "); got != "run console serve exec export import" {
		t.Errorf("nb: %q", got)
	}
	if got := strings.Join(complete("kernel", ""), " "); got != "install uninstall path" {
		t.Errorf("kernel: %q", got)
	}
	run := strings.Join(complete("nb", "run", "--"), " ")
	for _, want := range []string{"--force", "--dry-run", "--model", "--yes"} {
		if !strings.Contains(run, want) {
			t.Errorf("nb run lacks %s: %q", want, run)
		}
	}
	for _, not := range []string{"--state", "--port", "--open", "--any-kernel"} {
		if strings.Contains(run, not) {
			t.Errorf("nb run offers %s, which it refuses: %q", not, run)
		}
	}
	if serve := strings.Join(complete("nb", "serve", "--"), " "); !strings.Contains(serve, "--port") || strings.Contains(serve, "--force") {
		t.Errorf("nb serve: %q", serve)
	}
	if got := strings.Join(complete("kernel", "install", "--"), " "); !strings.Contains(got, "--python") || !strings.Contains(got, "--dir") {
		t.Errorf("kernel install: %q", got)
	}
	if got := strings.Join(complete("kernel", "path", "--"), " "); strings.Contains(got, "--python") {
		t.Errorf("kernel path: %q", got)
	}
}

// Every flag nb accepts for a subcommand is one it documents.
func TestNbFlagsAreDocumented(t *testing.T) {
	help := helpTopics["nb"]
	var documented strings.Builder
	for _, f := range help.flags {
		documented.WriteString(f.name + " ")
	}
	for sub, flags := range nbFlags {
		for _, f := range flags {
			if !strings.Contains(documented.String(), f) {
				t.Errorf("mote nb %s takes %s, which mote help nb does not describe", sub, f)
			}
		}
	}
}
