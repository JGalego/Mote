package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRememberAndForget(t *testing.T) {
	e := newEnv(t)
	e.setup()

	if code, out, errs := e.mote("", "remember", "I write Go and prefer short answers"); code != 0 || !strings.Contains(out, "remembered") {
		t.Fatalf("remember: %d %s %s", code, out, errs)
	}
	e.mote("", "remember", "the project is called mote")

	code, out, _ := e.mote("", "memory")
	if code != 0 || !strings.Contains(out, "I write Go") || !strings.Contains(out, "called mote") {
		t.Fatalf("memory: %d %s", code, out)
	}
	// History is off by default, and mote says so rather than staying mute.
	if !strings.Contains(out, "history off") {
		t.Errorf("memory does not mention that history is off: %s", out)
	}

	if code, _, _ := e.mote("", "forget", "1"); code != 0 {
		t.Fatal("forget 1 failed")
	}
	if _, out, _ := e.mote("", "memory"); strings.Contains(out, "I write Go") || !strings.Contains(out, "called mote") {
		t.Errorf("wrong fact forgotten: %s", out)
	}
	if code, _, errs := e.mote("", "forget", "9"); code == 0 || !strings.Contains(errs, "no fact 9") {
		t.Errorf("out of range: %d %s", code, errs)
	}
	if code, _, _ := e.mote("", "forget", "--all"); code != 0 {
		t.Fatal("forget --all failed")
	}
	if _, out, _ := e.mote("", "memory"); !strings.Contains(out, "no facts") {
		t.Errorf("facts survived: %s", out)
	}
	if code, _, _ := e.mote("", "remember"); code != ExitUsage {
		t.Error("empty remember should be a usage error")
	}
	if code, _, _ := e.mote("", "forget"); code != ExitUsage {
		t.Error("bare forget should be a usage error")
	}
}

func TestHistoryIsOptInAndRecalled(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")

	// Off by default: running a task records nothing.
	if code, _, errs := e.mote("", "run", "chat", "first question"); code != 0 {
		t.Fatalf("chat: %s", errs)
	}
	if _, out, _ := e.mote("", "memory"); strings.Contains(out, "first question") {
		t.Errorf("history recorded while off: %s", out)
	}

	if code, _, errs := e.mote("", "config", "set", "memory", "true"); code != 0 {
		t.Fatalf("config set memory: %s", errs)
	}
	if code, _, errs := e.mote("", "run", "chat", "second question"); code != 0 {
		t.Fatalf("chat: %s", errs)
	}
	_, out, _ := e.mote("", "memory")
	if !strings.Contains(out, "second question") {
		t.Errorf("history not recorded: %s", out)
	}

	// A follow-up with --continue runs and keeps recording.
	if code, _, errs := e.mote("", "run", "chat", "and a follow-up", "--continue"); code != 0 {
		t.Fatalf("--continue: %s", errs)
	}
	if _, out, _ := e.mote("", "memory"); !strings.Contains(out, "follow-up") {
		t.Errorf("follow-up not recorded: %s", out)
	}

	if code, _, _ := e.mote("", "forget", "--history"); code != 0 {
		t.Fatal("forget --history failed")
	}
	if _, out, _ := e.mote("", "memory"); strings.Contains(out, "second question") {
		t.Errorf("history survived: %s", out)
	}
}

func TestMemorySearch(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	e.install("bge-small-en-1.5")
	e.mote("", "config", "set", "memory", "true")
	e.mote("", "run", "chat", "how do I pickle a cucumber")
	e.mote("", "run", "chat", "write a go mutex example")

	code, out, errs := e.mote("", "memory", "search", "go mutex")
	if code != 0 {
		t.Fatalf("search: %d %s", code, errs)
	}
	if !strings.Contains(out, "mutex") {
		t.Errorf("search did not find the exchange: %s", out)
	}
	// The closest hit is listed first.
	if i, j := strings.Index(out, "mutex"), strings.Index(out, "cucumber"); j >= 0 && i > j {
		t.Errorf("hits are not ordered by closeness: %s", out)
	}
	if code, _, _ := e.mote("", "memory", "search"); code != ExitUsage {
		t.Error("search with no query should be a usage error")
	}
	if code, _, _ := e.mote("", "memory", "nonsense"); code != ExitUsage {
		t.Error("unknown subcommand should be a usage error")
	}

	// --recall reaches past the last exchange for a related one.
	if code, _, errs := e.mote("", "run", "chat", "remind me about locks", "--recall"); code != 0 {
		t.Errorf("--recall: %d %s", code, errs)
	}
}

func TestMemoryFilesAreTheUsersOwn(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.mote("", "remember", "a fact worth keeping")

	facts := filepath.Join(e.cfgDir, "facts.txt")
	b, err := os.ReadFile(facts)
	if err != nil {
		t.Fatalf("facts are not a plain file in the config dir: %v", err)
	}
	if strings.TrimSpace(string(b)) != "a fact worth keeping" {
		t.Errorf("facts file holds %q", b)
	}
	// Editing the file by hand is the same as using the commands.
	os.WriteFile(facts, []byte("edited by hand\n"), 0o600)
	if _, out, _ := e.mote("", "memory"); !strings.Contains(out, "edited by hand") {
		t.Errorf("hand-edited facts not read back: %s", out)
	}
}
