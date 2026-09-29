package cli

import (
	"strings"
	"testing"
)

// TestCommandHelpCoversEveryDispatchableCommand guards against a command
// being added to dispatch without a matching helpTopics entry, which would
// make `mote CMD --help` silently fall through to running the command.
func TestCommandHelpCoversEveryDispatchableCommand(t *testing.T) {
	for _, name := range topLevelCommands {
		if _, ok := helpTopics[name]; !ok {
			t.Errorf("topLevelCommands has %q but helpTopics does not document it", name)
		}
	}
	for name, h := range helpTopics {
		if h.summary == "" {
			t.Errorf("%s: missing summary", name)
		}
		if len(h.usage) == 0 {
			t.Errorf("%s: missing usage", name)
		}
		if len(h.examples) == 0 && name != "help" {
			t.Errorf("%s: missing examples", name)
		}
	}
}

func TestHelpCommandPrintsDetailForAKnownCommand(t *testing.T) {
	e := newEnv(t)
	code, out, _ := e.mote("", "help", "chat")
	if code != 0 {
		t.Fatalf("help chat: exit %d", code)
	}
	for _, want := range []string{"mote chat", "Usage:", "Flags:", "Examples:", "--system"} {
		if !strings.Contains(out, want) {
			t.Errorf("help chat missing %q in:\n%s", want, out)
		}
	}
}

func TestHelpCommandRejectsUnknownCommand(t *testing.T) {
	e := newEnv(t)
	code, _, errs := e.mote("", "help", "frobnicate")
	if code != ExitUsage || !strings.Contains(errs, "unknown command") {
		t.Errorf("help frobnicate: %d %q", code, errs)
	}
}

// TestPerCommandHelpFlagDoesNotRunTheCommand checks --help/-h short-circuits
// dispatch before the command's own handler runs, for commands that would
// otherwise need configuration, network access or arguments.
func TestPerCommandHelpFlagDoesNotRunTheCommand(t *testing.T) {
	e := newEnv(t) // no `mote setup`, so any real run would fail with ExitMissing
	cases := [][]string{
		{"chat", "--help"},
		{"run", "-h"},
		{"do", "--help"},
		{"agent", "-h"},
		{"models", "--help"},
		{"models", "pull", "--help"},
		{"config", "--help"},
		{"serve", "-h"},
	}
	for _, args := range cases {
		code, out, _ := e.mote("", args...)
		if code != 0 {
			t.Errorf("%v: exit %d", args, code)
		}
		if !strings.Contains(out, "Usage:") || !strings.Contains(out, "mote "+args[0]) {
			t.Errorf("%v: missing help content in %q", args, out)
		}
	}
}

func TestHasHelpFlagStopsAtDoubleDash(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"--help"}, true},
		{[]string{"-h"}, true},
		{[]string{"foo", "--help"}, true},
		{[]string{"--", "--help"}, false},
		{[]string{"foo", "bar"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := hasHelpFlag(c.args); got != c.want {
			t.Errorf("hasHelpFlag(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}
