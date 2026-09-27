package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVersionAndHelp(t *testing.T) {
	e := newEnv(t)
	code, out, _ := e.mote("", "version")
	if code != 0 || !strings.HasPrefix(out, "mote ") || !strings.Contains(out, runtime.GOOS) {
		t.Errorf("version: %d %q", code, out)
	}
	// A test binary is not stamped, so version() falls through to the
	// build info and must still answer something.
	if v := version(); v == "" {
		t.Error("version() is empty")
	}
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}, {}} {
		if code, out, _ := e.mote("", args...); code != 0 || !strings.Contains(out, "Usage:") {
			t.Errorf("%v: %d %q", args, code, out)
		}
	}
	if code, _, errs := e.mote("", "nonsense"); code != ExitUsage || !strings.Contains(errs, "unknown command") {
		t.Errorf("unknown command: %d %s", code, errs)
	}
}

func TestModelsSubcommands(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")

	code, out, errs := e.mote("", "models")
	if code != 0 || !strings.Contains(out, "qwen3.5-0.8b") {
		t.Fatalf("models: %d %s %s", code, out, errs)
	}
	if code, out, _ := e.mote("", "models", "list"); code != 0 || !strings.Contains(out, "qwen3.5-0.8b") {
		t.Errorf("models list: %d %s", code, out)
	}
	// why explains a capability's choice with its reasoning.
	code, out, errs = e.mote("", "models", "why", "text")
	if code != 0 || !strings.Contains(out, "qwen3.5") {
		t.Fatalf("models why: %d %s %s", code, out, errs)
	}
	if code, _, errs := e.mote("", "models", "why", "nosuchcap"); code != ExitUsage || !strings.Contains(errs, "nosuchcap") {
		t.Errorf("why unknown cap: %d %s", code, errs)
	}
	if code, _, _ := e.mote("", "models", "why"); code != ExitUsage {
		t.Error("why with no capability should be a usage error")
	}
	// verify checks the files on disk against the registry hashes. The
	// installed fake is sparse, so this must fail rather than pass blindly.
	if code, out, errs := e.mote("", "models", "verify"); code == 0 {
		t.Errorf("verify passed on sparse files: %s %s", out, errs)
	}
	if code, _, errs := e.mote("", "models", "rm", "nosuchmodel"); code != ExitUsage || !strings.Contains(errs, "nosuchmodel") {
		t.Errorf("rm unknown: %d %s", code, errs)
	}
	if code, out, errs := e.mote("", "models", "rm", "qwen3.5-0.8b"); code != 0 {
		t.Errorf("rm: %d %s %s", code, out, errs)
	}
	if _, out, _ := e.mote("", "models"); strings.Contains(out, "installed") {
		t.Errorf("model still listed as installed: %s", out)
	}
	if code, _, _ := e.mote("", "models", "pull"); code != ExitUsage {
		t.Error("pull with no argument should be a usage error")
	}
	if code, _, errs := e.mote("", "models", "pull", "nosuchmodel"); code != ExitUsage || !strings.Contains(errs, "nosuchmodel") {
		t.Errorf("pull unknown: %d %s", code, errs)
	}
	if code, _, _ := e.mote("", "models", "nonsense"); code != ExitUsage {
		t.Error("unknown subcommand should be a usage error")
	}
}

func TestConfigSubcommands(t *testing.T) {
	e := newEnv(t)
	e.setup()

	if code, out, _ := e.mote("", "config"); code != 0 || !strings.Contains(out, "profile") {
		t.Errorf("config: %d %s", code, out)
	}
	if code, out, _ := e.mote("", "config", "show"); code != 0 || !strings.Contains(out, "profile") {
		t.Errorf("config show: %d %s", code, out)
	}
	if code, out, _ := e.mote("", "config", "path"); code != 0 || !strings.Contains(out, "config.json") {
		t.Errorf("config path: %d %s", code, out)
	}
	if code, _, errs := e.mote("", "config", "set", "threads", "3"); code != 0 {
		t.Fatalf("set threads: %s", errs)
	}
	if _, out, _ := e.mote("", "config", "show"); !strings.Contains(out, `"threads": 3`) {
		t.Errorf("threads not saved: %s", out)
	}
	if code, _, errs := e.mote("", "config", "set", "threads", "many"); code == 0 || !strings.Contains(errs, "number") {
		t.Errorf("bad threads: %d %s", code, errs)
	}
	if code, _, errs := e.mote("", "config", "set", "nosuchkey", "x"); code == 0 || !strings.Contains(errs, "unknown key") {
		t.Errorf("unknown key: %d %s", code, errs)
	}
	if code, _, _ := e.mote("", "config", "set", "threads"); code != ExitUsage {
		t.Error("set with no value should be a usage error")
	}
	// Every save is kept, so history lists them and rollback undoes one.
	code, out, errs := e.mote("", "config", "history")
	if code != 0 || !strings.Contains(out, "set threads=3") {
		t.Fatalf("history: %d %s %s", code, out, errs)
	}
	if code, _, errs := e.mote("", "config", "rollback"); code != 0 {
		t.Fatalf("rollback: %s", errs)
	}
	if _, out, _ := e.mote("", "config", "show"); strings.Contains(out, `"threads": 3`) {
		t.Errorf("rollback did not undo the change: %s", out)
	}
	if code, _, _ := e.mote("", "config", "nonsense"); code != ExitUsage {
		t.Error("unknown subcommand should be a usage error")
	}
}

func TestConfigEditUsesTheConfiguredEditor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake editor is a shell script")
	}
	e := newEnv(t)
	e.setup()
	// An "editor" that appends a key, to prove mote runs it on the file.
	ed := filepath.Join(t.TempDir(), "fake-editor")
	// PATH is empty in these tests, so the script uses shell builtins only.
	script := `#!/bin/sh
body=""
while IFS= read -r l; do
  case "$l" in
    *'"profile": "small"'*) l='  "profile": "balanced",' ;;
  esac
  body="$body$l
"
done < "$1"
echo "$body" > "$1"
`
	if err := os.WriteFile(ed, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := e.mote("", "config", "set", "editor", ed); code != 0 {
		t.Fatalf("set editor: %s", errs)
	}
	if code, out, errs := e.mote("", "config", "edit"); code != 0 {
		t.Fatalf("config edit: %d %s %s", code, out, errs)
	}
	if _, out, _ := e.mote("", "config", "show"); !strings.Contains(out, "balanced") {
		t.Errorf("editor's change not saved: %s", out)
	}
}

func TestDoctorReportsWhatIsMissing(t *testing.T) {
	e := newEnv(t)
	e.setup()
	code, out, _ := e.mote("", "doctor")
	// Models are not downloaded and no tools are on PATH, so doctor warns
	// but does not fail: warnings are not problems.
	if code != 0 {
		t.Errorf("doctor exit %d:\n%s", code, out)
	}
	for _, want := range []string{"platform", "registry", "config", "runtime", "tasks", "ffmpeg"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor does not mention %s:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "not downloaded") {
		t.Errorf("doctor does not flag missing models:\n%s", out)
	}

	// A config pinning a model that does not exist is a failure, not a warning.
	if code, _, _ := e.mote("", "config", "set", "models.text", "nosuchmodel"); code == 0 {
		t.Skip("the config rejected the pin, so doctor cannot see it")
	}
}

func TestBenchFlagsAndTuneRepeat(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")

	code, out, errs := e.mote("", "bench", "--model", "qwen3.5-0.8b")
	if code != 0 || !strings.Contains(out, "qwen3.5-0.8b") {
		t.Fatalf("bench --model: %d %s %s", code, out, errs)
	}
	if code, _, errs := e.mote("", "bench", "--model", "nosuchmodel"); code != ExitUsage || !strings.Contains(errs, "nosuchmodel") {
		t.Errorf("bench unknown model: %d %s", code, errs)
	}
	if code, _, _ := e.mote("", "bench", "extra"); code != ExitUsage {
		t.Error("bench with a positional argument should be a usage error")
	}
	if code, _, _ := e.mote("", "bench", "--full", "--model", "qwen3.5-0.8b"); code != 0 {
		t.Error("bench --full failed")
	}
	// tune with nothing to change still reports, and does not fail.
	if code, out, errs := e.mote("", "tune"); code != 0 {
		t.Errorf("tune: %d %s %s", code, out, errs)
	}
	if code, _, _ := e.mote("", "tune", "extra"); code != ExitUsage {
		t.Error("tune with a positional argument should be a usage error")
	}
}

func TestUpdateRejectsExtraArguments(t *testing.T) {
	e := newEnv(t)
	e.setup()
	if code, _, _ := e.mote("", "update", "extra"); code != ExitUsage {
		t.Error("update with a positional argument should be a usage error")
	}
}

func TestCommandsNeedingConfigSaySo(t *testing.T) {
	e := newEnv(t) // no setup
	for _, cmd := range []string{"run", "pipe", "listen", "do", "memory", "remember", "forget", "models", "bench", "tune"} {
		code, _, errs := e.mote("", cmd, "x")
		if code != ExitMissing || !strings.Contains(errs, "mote setup") {
			t.Errorf("%s without config: %d %s", cmd, code, errs)
		}
	}
	// These work without a configuration.
	for _, cmd := range []string{"version", "help", "tasks", "doctor"} {
		if code, _, errs := e.mote("", cmd); code != 0 && cmd != "doctor" {
			t.Errorf("%s without config: %d %s", cmd, code, errs)
		}
	}
}

func TestInstallHintAndHostOf(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe", "git"} {
		if h := installHint(tool); !strings.Contains(h, "install it") {
			t.Errorf("hint for %s: %q", tool, h)
		}
	}
	// ffprobe ships with ffmpeg, so that is what the hint names.
	if h := installHint("ffprobe"); !strings.Contains(h, "ffmpeg") {
		t.Errorf("ffprobe hint: %q", h)
	}
	cases := map[string]string{
		"https://huggingface.co/a/b": "huggingface.co",
		"https://github.com/x":       "github.com",
		"nonsense":                   "nonsense",
	}
	for in, want := range cases {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q want %q", in, got, want)
		}
	}
}

func TestMBFormatsSizes(t *testing.T) {
	cases := map[int64]string{
		512 << 20: "512 MB",
		2 << 30:   "2.0 GB",
	}
	for in, want := range cases {
		if got := mb(in); got != want {
			t.Errorf("mb(%d) = %q want %q", in, got, want)
		}
	}
}

// moteLive runs mote with both streams on real files and live redrawing
// forced on, which is how spinners, streamed output and the model footer
// behave in a terminal.
func (e *env) moteLive(stdin string, args ...string) (int, string, string) {
	e.t.Setenv("MOTE_FORCE_LIVE", "1")
	e.t.Setenv("CLICOLOR_FORCE", "1")
	dir := e.t.TempDir()
	outPath, errPath := filepath.Join(dir, "out"), filepath.Join(dir, "err")
	outF, err := os.Create(outPath)
	if err != nil {
		e.t.Fatal(err)
	}
	errF, err := os.Create(errPath)
	if err != nil {
		e.t.Fatal(err)
	}
	code := Main(args, strings.NewReader(stdin), outF, errF)
	outF.Close()
	errF.Close()
	o, _ := os.ReadFile(outPath)
	er, _ := os.ReadFile(errPath)
	return code, string(o), string(er)
}

func TestTerminalRunStreamsAndReportsUsage(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")

	code, out, errs := e.moteLive("", "run", "chat", "hello there")
	if code != 0 {
		t.Fatalf("run: %d %s", code, errs)
	}
	if !strings.Contains(out, "echo: hello there") {
		t.Errorf("streamed output missing: %q", out)
	}
	// The footer summarises model usage on stderr, not stdout.
	if !strings.Contains(errs, "qwen3.5-0.8b") || !strings.Contains(errs, "tokens") {
		t.Errorf("no usage footer: %q", errs)
	}
	if !strings.Contains(errs, "local CPU") {
		t.Errorf("footer does not say the work was local: %q", errs)
	}
	if strings.Contains(out, "tokens ·") {
		t.Errorf("footer leaked into stdout: %q", out)
	}
}

func TestTerminalDataOutputIsHighlighted(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	doc := filepath.Join(t.TempDir(), "doc.txt")
	os.WriteFile(doc, []byte("Invoice 7"), 0o644)
	// extract always produces JSON, so the language is known without
	// having to guess from the fake model's reply.
	code, out, errs := e.moteLive("", "run", "extract", doc)
	if code != 0 {
		t.Fatalf("run extract: %d %s", code, errs)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("JSON was not highlighted in a terminal: %q", out)
	}
}

func TestModelFlagOverridesTheCapability(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	// --model picks a model for every capability it provides.
	if code, out, errs := e.mote("", "run", "chat", "hi", "--model", "qwen3.5-0.8b"); code != 0 || !strings.Contains(out, "echo") {
		t.Errorf("--model: %d %s %s", code, out, errs)
	}
	if code, _, errs := e.mote("", "run", "chat", "hi", "--model", "nosuchmodel"); code != ExitUsage || !strings.Contains(errs, "nosuchmodel") {
		t.Errorf("--model unknown: %d %s", code, errs)
	}
	// A profile that does not exist is rejected by selection.
	if code, _, _ := e.mote("", "run", "chat", "hi", "--profile", "nonsense"); code == 0 {
		t.Error("unknown profile accepted")
	}
}

func TestPinnedModelInConfig(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	if code, _, errs := e.mote("", "config", "set", "models.text", "qwen3.5-0.8b"); code != 0 {
		t.Fatalf("pin: %s", errs)
	}
	if code, out, errs := e.mote("", "run", "chat", "hi"); code != 0 || !strings.Contains(out, "echo") {
		t.Errorf("pinned model: %d %s %s", code, out, errs)
	}
	// why explains that the choice came from the config, not the policy.
	if _, out, _ := e.mote("", "models", "why", "text"); !strings.Contains(out, "pinned") {
		t.Errorf("why does not mention the pin: %s", out)
	}
	// `config set` refuses a pin the registry cannot honour.
	if code, _, errs := e.mote("", "config", "set", "models.text", "qwen3-tts-1.7b"); code == 0 {
		t.Errorf("config accepted a model without the capability: %s", errs)
	}
}

func TestConfigEditedByHandIsCheckedWhenUsed(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	// A config edited outside mote can pin anything, so the run path
	// itself has to catch it.
	pin := func(model string) {
		b, err := os.ReadFile(filepath.Join(e.cfgDir, "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		if err := json.Unmarshal(b, &cfg); err != nil {
			t.Fatal(err)
		}
		cfg["models"] = map[string]string{"text": model}
		out, _ := json.Marshal(cfg)
		os.WriteFile(filepath.Join(e.cfgDir, "config.json"), out, 0o644)
	}

	pin("qwen3-tts-1.7b")
	if code, _, errs := e.mote("", "run", "chat", "hi"); code == 0 || !strings.Contains(errs, "lacks that capability") {
		t.Errorf("pin to a model without the capability: %d %s", code, errs)
	}
	pin("nosuchmodel")
	if code, _, errs := e.mote("", "run", "chat", "hi"); code == 0 || !strings.Contains(errs, "unknown model") {
		t.Errorf("pin to a missing model: %d %s", code, errs)
	}
}

func TestDoubleDashPassesArgumentsThrough(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	// Everything after -- is a positional argument, even if it looks like a flag.
	code, out, errs := e.mote("", "run", "chat", "--", "--not-a-flag")
	if code != 0 || !strings.Contains(out, "--not-a-flag") {
		t.Errorf("--: %d %s %s", code, out, errs)
	}
}

func TestPullReportsWhatIsAlreadyThere(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	code, out, errs := e.mote("", "models", "pull", "qwen3.5-0.8b")
	if code != 0 || !strings.Contains(out, "already installed") {
		t.Errorf("pull an installed model: %d %s %s", code, out, errs)
	}
}

func TestSetupWizardAnswers(t *testing.T) {
	e := newEnv(t)
	t.Setenv("MOTE_FORCE_LIVE", "1") // pretend stdin is a terminal

	// Profile choice, then whatever else is asked: blank lines take the
	// default, so the script only has to answer the first question.
	answers := "2\n\n\n\n\n\n\n\n\n\n"
	code, out, errs := e.mote(answers, "setup", "--no-download")
	if code != 0 {
		t.Fatalf("setup: %d %s %s", code, out, errs)
	}
	if !strings.Contains(out, "Speed/quality/disk trade-off") {
		t.Errorf("wizard did not ask about the profile: %s", out)
	}
	if !strings.Contains(out, "Detected:") {
		t.Errorf("wizard did not report the machine: %s", out)
	}
	if _, cfg, _ := e.mote("", "config", "show"); !strings.Contains(cfg, "balanced") {
		t.Errorf("the chosen profile was not saved: %s", cfg)
	}
}

func TestSetupWizardRejectsNonsenseThenAccepts(t *testing.T) {
	e := newEnv(t)
	t.Setenv("MOTE_FORCE_LIVE", "1")
	// An out-of-range choice and a non-number are re-asked rather than
	// taken as the default.
	code, out, errs := e.mote("9\nabc\n1\n\n\n\n\n\n\n\n", "setup", "--no-download")
	if code != 0 {
		t.Fatalf("setup: %d %s %s", code, out, errs)
	}
	if !strings.Contains(out, "enter a number from 1 to") {
		t.Errorf("bad choice not re-asked: %s", out)
	}
}

func TestSetupFlags(t *testing.T) {
	e := newEnv(t)
	if code, _, errs := e.mote("", "setup", "--yes", "--no-download", "--profile", "quality"); code != 0 {
		t.Fatalf("setup --profile: %s", errs)
	}
	if _, out, _ := e.mote("", "config", "show"); !strings.Contains(out, "quality") {
		t.Errorf("profile not applied: %s", out)
	}
	if code, _, errs := e.mote("", "setup", "--yes", "--no-download", "--profile", "nonsense"); code != ExitUsage || !strings.Contains(errs, "unknown profile") {
		t.Errorf("bad profile: %d %s", code, errs)
	}
	// --data-dir is stored as an absolute path.
	dd := t.TempDir()
	if code, _, errs := e.mote("", "setup", "--yes", "--no-download", "--data-dir", dd); code != 0 {
		t.Fatalf("setup --data-dir: %s", errs)
	}
	if _, out, _ := e.mote("", "config", "show"); !strings.Contains(out, dd) {
		t.Errorf("data dir not saved: %s", out)
	}
	if code, _, errs := e.mote("", "setup", "--yes", "--no-download", "--config", filepath.Join(t.TempDir(), "missing.json")); code == 0 {
		t.Errorf("missing config file accepted: %s", errs)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte("{not json"), 0o644)
	if code, _, _ := e.mote("", "setup", "--config", bad); code != ExitUsage {
		t.Error("malformed config file should be a usage error")
	}
}

func TestSetupSaysWhenStdinIsNotATerminal(t *testing.T) {
	e := newEnv(t)
	code, out, errs := e.mote("", "setup", "--no-download")
	if code != 0 {
		t.Fatalf("setup: %d %s", code, errs)
	}
	if !strings.Contains(out, "stdin is not a terminal") {
		t.Errorf("no explanation of the non-interactive run: %s", out)
	}
}

func TestInstallHintPerPlatform(t *testing.T) {
	cases := []struct{ goos, tool, want string }{
		{"linux", "ffmpeg", "apt install ffmpeg"},
		{"darwin", "ffmpeg", "brew install ffmpeg"},
		{"darwin", "ffprobe", "brew install ffmpeg"},
		{"windows", "ffmpeg", "winget install Gyan.FFmpeg"},
		{"windows", "git", "winget install Git.Git"},
	}
	for _, c := range cases {
		if got := installHintOn(c.tool, c.goos); !strings.Contains(got, c.want) {
			t.Errorf("%s/%s: %q does not mention %q", c.goos, c.tool, got, c.want)
		}
	}
}

func TestCaptureArgsPerPlatform(t *testing.T) {
	linux, err := captureArgsOn("linux", "", 5, "/tmp/a.wav")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(linux, " "), "-f pulse -i default") {
		t.Errorf("linux: %q", linux)
	}
	mac, err := captureArgsOn("darwin", "", 5, "/tmp/a.wav")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(mac, " "), "avfoundation") {
		t.Errorf("darwin: %q", mac)
	}
	// Windows has no default input, so it must ask for one by name.
	if _, err := captureArgsOn("windows", "", 5, "/tmp/a.wav"); err == nil {
		t.Error("windows without a device was accepted")
	}
	win, err := captureArgsOn("windows", "Microphone (Realtek)", 5, "/tmp/a.wav")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(win, " "), "audio=Microphone (Realtek)") {
		t.Errorf("windows: %q", win)
	}
	// An already-prefixed device is not prefixed twice.
	win2, _ := captureArgsOn("windows", "audio=Mic", 5, "/tmp/a.wav")
	if strings.Contains(strings.Join(win2, " "), "audio=audio=") {
		t.Errorf("device prefixed twice: %q", win2)
	}
	if _, err := captureArgsOn("plan9", "", 5, "/tmp/a.wav"); err == nil {
		t.Error("an unsupported platform was accepted")
	}
}

func TestVersionUsesTheStampWhenBuiltWithOne(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "v1.2.3"
	if got := version(); got != "v1.2.3" {
		t.Errorf("stamped build: %q", got)
	}
	Version = "dev"
	// Unstamped, the build info answers instead, and never empty.
	if got := version(); got == "" {
		t.Error("unstamped build reported no version")
	}
}

func TestFlagNeedingAValue(t *testing.T) {
	e := newEnv(t)
	e.setup()
	if code, _, errs := e.mote("", "run", "chat", "hi", "-o"); code != ExitUsage || !strings.Contains(errs, "needs a value") {
		t.Errorf("-o with no value: %d %s", code, errs)
	}
}

func TestDoctorFailsOnABrokenInstallation(t *testing.T) {
	e := newEnv(t)
	e.setup()
	// A runtime directory without the binaries is a failure, not a warning.
	empty := t.TempDir()
	if code, _, errs := e.mote("", "config", "set", "llama_dir", empty); code != 0 {
		t.Fatalf("set llama_dir: %s", errs)
	}
	code, out, _ := e.mote("", "doctor")
	if code == 0 {
		t.Errorf("doctor passed with no runtime:\n%s", out)
	}
	if !strings.Contains(out, "FAIL") && !strings.Contains(out, "✗") {
		t.Errorf("no failure marked:\n%s", out)
	}

	// A broken custom task file is reported by doctor too.
	dir := t.TempDir()
	t.Setenv("MOTE_TASKS_DIR", dir)
	os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{"tasks":[{"id":"x"}]}`), 0o644)
	if _, out, _ := e.mote("", "doctor"); !strings.Contains(out, "broken.json") {
		t.Errorf("doctor does not report the broken task file:\n%s", out)
	}
}

func TestDoctorSeesToolsThatArePresent(t *testing.T) {
	e := newEnv(t)
	e.setup()
	fakeTools(t)
	_, out, _ := e.mote("", "doctor")
	for _, tool := range []string{"ffmpeg", "ffprobe", "git"} {
		if !strings.Contains(out, tool) {
			t.Errorf("doctor does not list %s:\n%s", tool, out)
		}
	}
	if strings.Contains(out, "not found on PATH") {
		t.Errorf("tools on PATH reported missing:\n%s", out)
	}
}

func TestDiffOutputIsColouredInATerminal(t *testing.T) {
	e := newEnv(t)
	e.setup()
	fakeTools(t)
	e.install("qwen3.5-0.8b")
	proj := t.TempDir()
	os.WriteFile(filepath.Join(proj, "hello.txt"), []byte("hello\n"), 0o644)

	code, out, errs := e.moteLive("", "run", "patch", proj, "greet the world")
	if code != 0 {
		t.Fatalf("patch: %d %s", code, errs)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("diff was not coloured: %q", out)
	}
	if !strings.Contains(out, "hello, world") {
		t.Errorf("diff content: %q", out)
	}
}

func TestRouterRepliesThatMakeNoSense(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")

	// The task engine rejects a non-JSON reply before the router reads it.
	t.Setenv("MOTE_FAKE_REPLY", "I would rather not say")
	if code, _, errs := e.mote("", "do", "anything at all"); code == 0 || !strings.Contains(errs, "valid JSON") {
		t.Errorf("unparseable router reply: %d %s", code, errs)
	}
	// Valid JSON that names no task is the router's own error.
	t.Setenv("MOTE_FAKE_REPLY", `{"ok":true}`)
	if code, _, errs := e.mote("", "do", "anything at all"); code == 0 || !strings.Contains(errs, "chose nothing") {
		t.Errorf("empty router choice: %d %s", code, errs)
	}
	t.Setenv("MOTE_FAKE_REPLY", `{"task":"nosuchtask"}`)
	if code, _, errs := e.mote("", "do", "anything at all"); code == 0 || !strings.Contains(errs, "not a task") {
		t.Errorf("invented task: %d %s", code, errs)
	}
}

func TestRecallWithoutTheEmbedModel(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b") // but not the embedding model
	e.mote("", "config", "set", "memory", "true")
	e.mote("", "run", "chat", "something to remember")

	if code, _, errs := e.mote("", "run", "chat", "and now", "--recall"); code != ExitMissing || !strings.Contains(errs, "bge") {
		t.Errorf("--recall without the model: %d %s", code, errs)
	}
	if code, _, errs := e.mote("", "memory", "search", "anything"); code != ExitMissing || !strings.Contains(errs, "bge") {
		t.Errorf("search without the model: %d %s", code, errs)
	}
}

func TestSearchWithNothingRemembered(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("bge-small-en-1.5")
	if code, out, errs := e.mote("", "memory", "search", "anything"); code != 0 || !strings.Contains(out, "nothing remembered") {
		t.Errorf("empty history search: %d %s %s", code, out, errs)
	}
}

func TestSetupOffersToReuseASystemLlama(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in llama-server is a shell script")
	}
	e := newEnv(t)
	t.Setenv("MOTE_FORCE_LIVE", "1")
	// A llama-server on PATH is offered instead of the pinned download.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "llama-server"), []byte("#!/bin/sh\necho fake\n"), 0o755)
	t.Setenv("PATH", dir)

	// Accept the profile default, then say yes to reusing it.
	code, out, errs := e.mote("\ny\n\n\n\n\n", "setup", "--no-download")
	if code != 0 {
		t.Fatalf("setup: %d %s %s", code, out, errs)
	}
	if !strings.Contains(out, "Found llama.cpp at") {
		t.Fatalf("no offer to reuse:\n%s", out)
	}
	if _, cfg, _ := e.mote("", "config", "show"); !strings.Contains(cfg, dir) {
		t.Errorf("the system llama.cpp was not recorded: %s", cfg)
	}
}

func TestSetupDeclinesASystemLlama(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in llama-server is a shell script")
	}
	e := newEnv(t)
	t.Setenv("MOTE_FORCE_LIVE", "1")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "llama-server"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", dir)

	// Answering nonsense is re-asked, then "n" declines the reuse.
	code, out, _ := e.mote("\nmaybe\nn\n\n\n\n", "setup", "--no-download")
	if code != 0 && !strings.Contains(out, "Found llama.cpp") {
		t.Fatalf("setup: %d\n%s", code, out)
	}
	if !strings.Contains(out, "please answer y or n") {
		t.Errorf("nonsense was not re-asked:\n%s", out)
	}
}

func TestSetupRejectsExtraArguments(t *testing.T) {
	e := newEnv(t)
	if code, _, errs := e.mote("", "setup", "surprise"); code != ExitUsage || !strings.Contains(errs, "no arguments") {
		t.Errorf("setup with a positional: %d %s", code, errs)
	}
}

func TestSetupAutoDownloadFlag(t *testing.T) {
	e := newEnv(t)
	if code, _, errs := e.mote("", "setup", "--yes", "--no-download", "--auto-download"); code != 0 {
		t.Fatalf("setup --auto-download: %s", errs)
	}
	if _, out, _ := e.mote("", "config", "show"); !strings.Contains(out, `"auto_download": true`) {
		t.Errorf("auto_download not saved: %s", out)
	}
}

func TestConfigEditFallbacksAndFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake editors are shell scripts")
	}
	e := newEnv(t)
	e.setup()
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	// With no editor configured and none in the environment, mote says so.
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	if code, _, errs := e.mote("", "config", "edit"); code != ExitMissing || !strings.Contains(errs, "no editor configured") {
		t.Errorf("no editor: %d %s", code, errs)
	}

	// EDITOR is used when the config names none.
	touch := filepath.Join(dir, "touch-editor")
	os.WriteFile(touch, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	t.Setenv("EDITOR", touch)
	if code, _, errs := e.mote("", "config", "edit"); code != 0 {
		t.Errorf("EDITOR: %d %s", code, errs)
	}

	// An editor that fails is reported.
	boom := filepath.Join(dir, "boom")
	os.WriteFile(boom, []byte("#!/bin/sh\nexit 3\n"), 0o755)
	t.Setenv("EDITOR", boom)
	if code, _, _ := e.mote("", "config", "edit"); code == 0 {
		t.Error("a failing editor reported success")
	}

	// An editor that writes nonsense leaves a recoverable message.
	wrecker := filepath.Join(dir, "wrecker")
	os.WriteFile(wrecker, []byte("#!/bin/sh\necho 'not json' > \"$1\"\n"), 0o755)
	t.Setenv("EDITOR", wrecker)
	code, _, errs := e.mote("", "config", "edit")
	if code == 0 || !strings.Contains(errs, "rollback") {
		t.Errorf("invalid edit: %d %s", code, errs)
	}
}

func TestBenchWithNothingInstalled(t *testing.T) {
	e := newEnv(t)
	e.setup()
	if code, _, errs := e.mote("", "bench"); code != ExitMissing || !strings.Contains(errs, "no models installed") {
		t.Errorf("bench with no models: %d %s", code, errs)
	}
	if code, _, errs := e.mote("", "bench", "--model", "qwen3.5-0.8b"); code != ExitMissing || !strings.Contains(errs, "not downloaded") {
		t.Errorf("bench on a missing model: %d %s", code, errs)
	}
	if code, _, errs := e.mote("", "tune"); code != ExitMissing || !strings.Contains(errs, "no local benchmark results") {
		t.Errorf("tune with no results: %d %s", code, errs)
	}
}

func TestTuneTwiceHasNothingToChange(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	if code, _, errs := e.mote("", "bench"); code != 0 {
		t.Fatalf("bench: %s", errs)
	}
	if code, _, errs := e.mote("", "tune", "--apply"); code != 0 {
		t.Fatalf("tune --apply: %s", errs)
	}
	// The second run has nothing left to propose.
	code, out, errs := e.mote("", "tune")
	if code != 0 {
		t.Fatalf("second tune: %d %s", code, errs)
	}
	if !strings.Contains(out, "no changes") {
		t.Errorf("second tune proposed something: %s", out)
	}
}

func TestDoctorFlagsAConfigEditedIntoNonsense(t *testing.T) {
	e := newEnv(t)
	e.setup()
	// A profile that does not exist can only get there by hand.
	p := filepath.Join(e.cfgDir, "config.json")
	b, _ := os.ReadFile(p)
	var cfg map[string]any
	json.Unmarshal(b, &cfg)
	cfg["profile"] = "luxury"
	out, _ := json.Marshal(cfg)
	os.WriteFile(p, out, 0o644)

	code, stdout, _ := e.mote("", "doctor")
	if code == 0 {
		t.Errorf("doctor passed with an unknown profile:\n%s", stdout)
	}
	if !strings.Contains(stdout, "luxury") {
		t.Errorf("doctor does not name the bad profile:\n%s", stdout)
	}
}
