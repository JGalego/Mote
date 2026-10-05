package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeClaude puts a claude on PATH that prints its environment and
// arguments, then exits with the given code.
func fakeClaude(t *testing.T, exit string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude is a shell script")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
printf 'base=%s\n' "$ANTHROPIC_BASE_URL"
printf 'key=%s\n' "$ANTHROPIC_API_KEY"
printf 'auth=%s\n' "$ANTHROPIC_AUTH_TOKEN"
printf 'oauth=%s\n' "$CLAUDE_CODE_OAUTH_TOKEN"
printf 'model=%s\n' "$ANTHROPIC_MODEL"
printf 'fable=%s\n' "$ANTHROPIC_DEFAULT_FABLE_MODEL"
printf 'opus=%s\n' "$ANTHROPIC_DEFAULT_OPUS_MODEL"
printf 'sonnet=%s\n' "$ANTHROPIC_DEFAULT_SONNET_MODEL"
printf 'haiku=%s\n' "$ANTHROPIC_DEFAULT_HAIKU_MODEL"
printf 'subagent=%s\n' "$CLAUDE_CODE_SUBAGENT_MODEL"
printf 'context=%s\n' "$CLAUDE_CODE_MAX_CONTEXT_TOKENS"
printf 'output=%s\n' "$CLAUDE_CODE_MAX_OUTPUT_TOKENS"
printf 'local=%s\n' "$CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"
for arg do printf 'arg=<%s>\n' "$arg"; done
exit ` + exit + `
`
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestClaudeLaunchesAgainstMoteServer(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	fakeClaude(t, "0")
	t.Setenv("ANTHROPIC_API_KEY", "remote-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "remote-token")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-token")

	code, out, errs := e.mote("", "claude", "--append-system-prompt", "Stay local")
	if code != 0 {
		t.Fatalf("claude: %d %s %s", code, out, errs)
	}
	for _, want := range []string{
		"base=http://127.0.0.1:", "key=\n", "auth=mote-local\n", "oauth=\n",
		"model=qwen3.5-0.8b\n", "fable=qwen3.5-0.8b\n", "opus=qwen3.5-0.8b\n", "sonnet=qwen3.5-0.8b\n",
		"haiku=qwen3.5-0.8b\n", "subagent=qwen3.5-0.8b\n",
		"context=32768\n", "output=8192\n", "local=1\n",
		"arg=<--bare>\narg=<--settings>\narg=<" + claudeSettings + ">\narg=<--append-system-prompt>\narg=<Stay local>\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errs, "loading qwen3.5-0.8b with a 32K context") {
		t.Errorf("stderr: %s", errs)
	}
}

func TestClaudeKeepsTheUsersOutputLimit(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	fakeClaude(t, "0")
	t.Setenv("CLAUDE_CODE_MAX_OUTPUT_TOKENS", "4096")
	code, out, errs := e.mote("", "claude", "--model", "qwen3.5-0.8b", "--print", "hi")
	if code != 0 || !strings.Contains(out, "output=4096\n") || !strings.Contains(out, "arg=<--model>\narg=<qwen3.5-0.8b>\n") {
		t.Errorf("claude: %d %s %s", code, out, errs)
	}
}

func TestClaudeExitsAsClaudeDid(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	fakeClaude(t, "7")
	code, _, errs := e.mote("", "claude", "--print", "hi")
	if code != 7 || strings.Contains(errs, "exit status") {
		t.Errorf("exit %d, stderr %q", code, errs)
	}
}

func TestClaudeRejectsAModelItCannotRun(t *testing.T) {
	e := newEnv(t)
	e.setup()
	fakeClaude(t, "0")
	code, _, errs := e.mote("", "claude", "--model", "nonexistent")
	if code != ExitUsage || !strings.Contains(errs, "unknown model") {
		t.Errorf("exit %d, stderr %q", code, errs)
	}
}

func TestClaudeChildArgs(t *testing.T) {
	for _, c := range []struct{ args, want []string }{
		{[]string{"--print", "hello"}, []string{"--bare", "--settings", claudeSettings, "--print", "hello"}},
		{[]string{"--no-bare", "--print", "hello"}, []string{"--settings", claudeSettings, "--print", "hello"}},
		{[]string{"--print", "--", "--no-bare"}, []string{"--bare", "--settings", claudeSettings, "--print", "--", "--no-bare"}},
		// The user's own permission mode or settings are left to them.
		{[]string{"--permission-mode", "auto"}, []string{"--bare", "--permission-mode", "auto"}},
		{[]string{"--bare", "--settings=s.json"}, []string{"--bare", "--settings=s.json"}},
	} {
		got := strings.Join(claudeChildArgs(c.args), " ")
		if want := strings.Join(c.want, " "); got != want {
			t.Errorf("claudeChildArgs(%q) = %q, want %q", c.args, got, want)
		}
	}
}

func TestClaudeModelArg(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "code"},
		{[]string{"--model", "text"}, "text"},
		{[]string{"--model=qwen3-4b-2507", "-p", "hi"}, "qwen3-4b-2507"},
		{[]string{"-p", "--", "--model", "text"}, "code"},
	} {
		if got := claudeModelArg(c.args); got != c.want {
			t.Errorf("claudeModelArg(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestClaudeNeedsTheExecutable(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	code, _, errs := e.mote("", "claude")
	if code != ExitMissing || !strings.Contains(errs, "Claude Code") {
		t.Errorf("missing claude: %d %s", code, errs)
	}
}
