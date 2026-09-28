package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jgalego/mote/internal/fakellama"
	"github.com/jgalego/mote/internal/fakemcp"
	"github.com/jgalego/mote/internal/ui"
	"github.com/jgalego/mote/registry"
)

func TestMain(m *testing.M) {
	fakellama.MaybeRun()
	fakemcp.MaybeRun()
	// A command that starts mote serve runs its own executable, which in
	// tests is this binary; this makes it mote.
	if os.Getenv("MOTE_TEST_AS_MOTE") == "1" {
		os.Exit(Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

type env struct {
	t        *testing.T
	home     string
	cfgDir   string
	llamaDir string
}

func newEnv(t *testing.T) *env {
	e := &env{t: t, home: t.TempDir(), cfgDir: t.TempDir(), llamaDir: t.TempDir()}
	t.Setenv("MOTE_HOME", e.home)
	t.Setenv("MOTE_CONFIG_DIR", e.cfgDir)
	t.Setenv("PATH", "") // no system tools or llama.cpp: deterministic
	// Models load in the command itself unless a test is about the
	// resident server.
	t.Setenv("MOTE_KEEP_ALIVE", "0")
	fakellama.Install(t, e.llamaDir)
	return e
}

func (e *env) mote(stdin string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Main(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func (e *env) setup() {
	cfg := filepath.Join(e.t.TempDir(), "c.json")
	os.WriteFile(cfg, []byte(`{"schema":1,"profile":"small","reuse_tools":true,"llama_dir":`+jsonStr(e.llamaDir)+`}`), 0o644)
	if code, out, errs := e.mote("", "setup", "--config", cfg, "--no-download"); code != 0 {
		e.t.Fatalf("setup: %d %s %s", code, out, errs)
	}
}

// install fakes a model download with sparse files of the right size.
func (e *env) install(id string) {
	m, ok := registry.Default().Model(id)
	if !ok {
		e.t.Fatalf("no model %s", id)
	}
	for _, f := range m.Files {
		p := filepath.Join(e.home, "models", id, f.Name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		fh, _ := os.Create(p)
		fh.Truncate(f.Size)
		fh.Close()
	}
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestUsageAndExitCodes(t *testing.T) {
	e := newEnv(t)
	if code, out, _ := e.mote(""); code != 0 || !strings.Contains(out, "mote run TASK") {
		t.Errorf("help: %d %s", code, out)
	}
	if code, _, _ := e.mote("", "frobnicate"); code != ExitUsage {
		t.Errorf("unknown command exit %d", code)
	}
	if code, _, errs := e.mote("", "run", "chat", "hi"); code != ExitMissing || !strings.Contains(errs, "mote setup") {
		t.Errorf("unconfigured: %d %s", code, errs)
	}
	if code, out, _ := e.mote("", "version"); code != 0 || !strings.Contains(out, "registry") {
		t.Errorf("version %d %s", code, out)
	}
}

func TestSetupIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.setup()
	code, out, _ := e.mote("", "config", "history")
	if code != 0 || strings.Count(out, "setup") != 1 {
		t.Errorf("setup re-run added history: %s", out)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte(`{"schema":1,"profile":"turbo"}`), 0o644)
	if code, _, errs := e.mote("", "setup", "--config", bad); code != ExitUsage || !strings.Contains(errs, "turbo") {
		t.Errorf("bad profile: %d %s", code, errs)
	}
}

func TestSetupInteractiveDefaults(t *testing.T) {
	e := newEnv(t)
	// Pre-place the runtime so setup does not download it.
	reg := registry.Default()
	rt := filepath.Join(e.home, "runtime", "llama.cpp-"+reg.Runtime.Version)
	os.MkdirAll(rt, 0o755)
	fakellama.Install(t, rt)
	code, out, errs := e.mote("", "setup", "--no-download")
	if code != 0 {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	if !strings.Contains(out, "Detected:") || !strings.Contains(out, "Ready.") {
		t.Errorf("output: %s", out)
	}
}

func TestRunChatAndMissingModel(t *testing.T) {
	e := newEnv(t)
	e.setup()
	code, _, errs := e.mote("", "run", "chat", "hello")
	if code != ExitMissing || !strings.Contains(errs, "mote models pull qwen3.5-0.8b") {
		t.Fatalf("missing model: %d %s", code, errs)
	}
	e.install("qwen3.5-0.8b")
	code, out, errs := e.mote("", "run", "chat", "hello")
	if code != 0 || strings.TrimSpace(out) != "echo: hello" {
		t.Fatalf("chat: %d %q %s", code, out, errs)
	}
	code, out, _ = e.mote("from stdin", "run", "chat", "-")
	if code != 0 || strings.TrimSpace(out) != "echo: from stdin" {
		t.Errorf("stdin: %d %q", code, out)
	}
	if code, _, _ := e.mote("", "run", "chat"); code != ExitUsage {
		t.Errorf("missing arg exit %d", code)
	}
	if code, _, _ := e.mote("", "run", "nope"); code != ExitUsage {
		t.Errorf("unknown task exit %d", code)
	}
	if code, _, _ := e.mote("", "run", "chat", "x", "--bogus"); code != ExitUsage {
		t.Errorf("unknown flag exit %d", code)
	}
}

func TestRunOutputsAndPatch(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	e.install("qwen3-tts-1.7b")
	dir := t.TempDir()
	doc := filepath.Join(dir, "doc.txt")
	os.WriteFile(doc, []byte("Invoice 7"), 0o644)
	out := filepath.Join(dir, "out.json")
	if code, _, errs := e.mote("", "run", "extract", doc, "-o", out); code != 0 {
		t.Fatalf("extract: %s", errs)
	}
	if b, _ := os.ReadFile(out); !strings.Contains(string(b), `"ok": true`) {
		t.Errorf("extract output %s", b)
	}
	wav := filepath.Join(dir, "s.wav")
	if code, stdout, errs := e.mote("", "run", "speak", "hi", "-o", wav); code != 0 || strings.TrimSpace(stdout) != wav {
		t.Errorf("speak: %d %s %s", code, stdout, errs)
	}
	proj := t.TempDir()
	os.WriteFile(filepath.Join(proj, "hello.txt"), []byte("hello\n"), 0o644)
	code, stdout, errs := e.mote("", "run", "patch", proj, "greet the world", "--apply")
	if code != 0 || !strings.Contains(stdout, "+hello, world") {
		t.Fatalf("patch: %d %s %s", code, stdout, errs)
	}
	if b, _ := os.ReadFile(filepath.Join(proj, "hello.txt")); string(b) != "hello, world\n" {
		t.Errorf("not applied: %q", b)
	}
	// Video tasks need ffmpeg, which is not on PATH in tests.
	vid := filepath.Join(dir, "v.mp4")
	os.WriteFile(vid, []byte("x"), 0o644)
	if code, _, errs := e.mote("", "run", "frames", vid); code != ExitMissing || !strings.Contains(errs, "ffprobe") {
		t.Errorf("missing tool: %d %s", code, errs)
	}
}

func TestModelsConfigAndDoctor(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	code, out, _ := e.mote("", "models")
	if code != 0 || !strings.Contains(out, "qwen3.5-0.8b") || !strings.Contains(out, "installed, default for") {
		t.Errorf("models: %s", out)
	}
	code, out, _ = e.mote("", "models", "why", "text")
	if code != 0 || !strings.Contains(out, "upstream TIGER-Lab/MMLU-Pro") {
		t.Errorf("why: %s", out)
	}
	if code, _, _ := e.mote("", "models", "why", "telepathy"); code != ExitUsage {
		t.Error("unknown capability accepted")
	}
	if code, _, errs := e.mote("", "config", "set", "models.text", "qwen3-asr-0.6b"); code != ExitUsage || !strings.Contains(errs, "does not provide") {
		t.Errorf("bad pin: %d %s", code, errs)
	}
	if code, _, _ := e.mote("", "config", "set", "profile", "balanced"); code != 0 {
		t.Error("set profile failed")
	}
	_, out, _ = e.mote("", "models", "why", "text")
	if !strings.Contains(out, "qwen3.5-2b") {
		t.Errorf("profile change not reflected: %s", out)
	}
	if code, _, _ := e.mote("", "config", "rollback"); code != 0 {
		t.Error("rollback failed")
	}
	_, out, _ = e.mote("", "config", "show")
	if !strings.Contains(out, `"profile": "small"`) {
		t.Errorf("rollback not applied: %s", out)
	}
	code, out, _ = e.mote("", "doctor")
	if code != 0 || !strings.Contains(out, "ok    runtime") || !strings.Contains(out, "not downloaded") {
		t.Errorf("doctor: %d %s", code, out)
	}
	code, out, _ = e.mote("", "tasks")
	if code != 0 || !strings.Contains(out, "transcribe") || !strings.Contains(out, "ffmpeg: missing") {
		t.Errorf("tasks: %s", out)
	}
}

func TestBenchAndTune(t *testing.T) {
	e := newEnv(t)
	e.setup()
	if code, _, _ := e.mote("", "tune"); code != ExitMissing {
		t.Errorf("tune without results exit %d", code)
	}
	e.install("qwen3.5-0.8b")
	code, out, errs := e.mote("", "bench")
	// The fake echoes prompts, so most checks fail; the run itself must work.
	if code != 0 || !strings.Contains(out, "qwen3.5-0.8b") {
		t.Fatalf("bench: %d %s %s", code, out, errs)
	}
	// --strict turns those same failing checks into a non-zero exit, for CI.
	if code, _, errs := e.mote("", "bench", "--strict"); code == 0 || !strings.Contains(errs, "qwen3.5-0.8b") {
		t.Errorf("bench --strict: %d %s", code, errs)
	}
	code, out, _ = e.mote("", "tune", "--apply")
	if code != 0 {
		t.Fatalf("tune: %s", out)
	}
	// Failing local checks must move text away from the 0.8b model, reviewably.
	if !strings.Contains(out, "text: qwen3.5-0.8b ->") || !strings.Contains(out, "applied") {
		t.Errorf("tune output: %s", out)
	}
	_, hist, _ := e.mote("", "config", "history")
	if !strings.Contains(hist, "tune:") {
		t.Errorf("history: %s", hist)
	}
}

func TestUpdate(t *testing.T) {
	e := newEnv(t)
	e.setup()
	reg := registry.Default()
	next := *reg
	next.Version = reg.Version + ".9"
	body := registry.Encode(&next)
	bad := []byte(`{"schema":1,"version":"9999"}`)
	serve := body
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(serve) }))
	defer srv.Close()
	t.Setenv("MOTE_REGISTRY_URL", srv.URL)

	if code, out, _ := e.mote("", "update", "--check"); code != 0 || !strings.Contains(out, "->") {
		t.Errorf("check: %s", out)
	}
	if _, err := os.Stat(filepath.Join(e.home, "registry", "models.json")); err == nil {
		t.Error("--check wrote the registry")
	}
	if code, _, errs := e.mote("", "update"); code != 0 {
		t.Fatalf("update: %s", errs)
	}
	if _, out, _ := e.mote("", "version"); !strings.Contains(out, next.Version) {
		t.Errorf("new registry not used: %s", out)
	}
	if _, out, _ := e.mote("", "update"); !strings.Contains(out, "is current") {
		t.Errorf("second update: %s", out)
	}
	serve = bad
	os.Remove(filepath.Join(e.home, "registry", "models.json"))
	if code, _, errs := e.mote("", "update"); code == 0 || !strings.Contains(errs, "rejected") {
		t.Errorf("invalid registry accepted: %s", errs)
	}
	t.Setenv("MOTE_REGISTRY_URL", "http://example.org/models.json")
	if code, _, errs := e.mote("", "update"); code == 0 || !strings.Contains(errs, "https") {
		t.Errorf("plain http accepted: %s", errs)
	}
}

// moteTTY runs mote with stdout on a real file, which ui treats as
// colourable when CLICOLOR_FORCE is set (a bytes.Buffer never is). Windows
// needs a real console handle for that, so the caller is told whether colour
// was actually on.
func (e *env) moteTTY(args ...string) (code int, out string, coloured bool) {
	p := filepath.Join(e.t.TempDir(), "stdout")
	f, err := os.Create(p)
	if err != nil {
		e.t.Fatal(err)
	}
	coloured = ui.New(f).Color()
	var errb bytes.Buffer
	code = Main(args, strings.NewReader(""), f, &errb)
	f.Close()
	b, _ := os.ReadFile(p)
	return code, string(b), coloured
}

func TestRunHighlightsCodeOnlyForTerminals(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	dir := t.TempDir()
	doc := filepath.Join(dir, "doc.txt")
	os.WriteFile(doc, []byte("Invoice 7"), 0o644)

	code, plain, errs := e.mote("", "run", "extract", doc)
	if code != 0 {
		t.Fatalf("extract: %d %s", code, errs)
	}
	if strings.Contains(plain, "\x1b[") {
		t.Errorf("piped output is coloured: %q", plain)
	}

	// A file keeps the exact bytes: highlighting must not leak into -o.
	out := filepath.Join(dir, "out.json")
	if code, _, errs := e.mote("", "run", "extract", doc, "-o", out); code != 0 {
		t.Fatalf("extract -o: %s", errs)
	}
	if b, _ := os.ReadFile(out); strings.Contains(string(b), "\x1b[") {
		t.Errorf("colour written to file: %q", b)
	}

	t.Setenv("CLICOLOR_FORCE", "1")
	code, coloured, hasColour := e.moteTTY("run", "extract", doc)
	if code != 0 {
		t.Fatalf("extract with colour: %d", code)
	}
	if !hasColour {
		// Windows colours console handles only; internal/ui covers the
		// highlighting itself on every platform.
		t.Skip("this platform does not colour a plain file handle")
	}
	if !strings.Contains(coloured, "\x1b[") {
		t.Errorf("terminal output is not highlighted: %q", coloured)
	}
	if got := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(coloured, ""); got != plain {
		t.Errorf("highlighting changed the text:\n got %q\nwant %q", got, plain)
	}
}

func TestCustomTasks(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	dir := t.TempDir()
	t.Setenv("MOTE_TASKS_DIR", dir)
	os.WriteFile(filepath.Join(dir, "shout.json"), []byte(`{"tasks":[{"id":"shout",
		"summary":"Answer loudly","in":["text"],"out":"text",
		"params":[{"name":"prompt","kind":"text"}],
		"steps":[{"op":"generate","cap":"text","prompt":"{{prompt}} IN CAPITALS","as":"out"}]}]}`), 0o644)

	code, out, errs := e.mote("", "tasks")
	if code != 0 || !strings.Contains(out, "shout") || !strings.Contains(out, "custom") {
		t.Fatalf("tasks: %d %s %s", code, out, errs)
	}
	code, out, errs = e.mote("", "run", "shout", "hello")
	if code != 0 || !strings.Contains(out, "hello IN CAPITALS") {
		t.Fatalf("run custom: %d %q %s", code, out, errs)
	}
	if code, out, _ := e.mote("", "doctor"); !strings.Contains(out, "1 custom") {
		t.Errorf("doctor does not report custom tasks (%d): %s", code, out)
	}

	// A broken file is named rather than silently ignored.
	os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{"tasks":[{"id":"nope"}]}`), 0o644)
	if code, _, errs := e.mote("", "run", "chat", "hi"); code == 0 || !strings.Contains(errs, "broken.json") {
		t.Errorf("broken task file: %d %s", code, errs)
	}
}

func TestLangHint(t *testing.T) {
	dir := t.TempDir()
	py := filepath.Join(dir, "slugify.py")
	os.WriteFile(py, []byte("x = 1\n"), 0o644)
	cases := map[string][]string{
		"python":     {"Python function that checks for palindromes"},
		"go":         {"a Go function Reverse(s string) string"},
		"":           {"go through the list and explain it"},
		"rust":       {"write it in Rust, please"},
		"cpp":        {"a C++ class for a stack"},
		"bash":       {"a shell script that backs up ~/notes"},
		"python ":    {py, "add type hints"},
		"javascript": {"port this to JavaScript"},
	}
	for want, args := range cases {
		if got := langHint(args); got != strings.TrimSpace(want) {
			t.Errorf("%q: %q, want %q", args, got, strings.TrimSpace(want))
		}
	}
	// A file's extension wins over words in the instruction.
	if got := langHint([]string{py, "rewrite this in Rust"}); got != "python" {
		t.Errorf("file then words: %q", got)
	}
}

// Small models told to answer with code only often leave out the fence that
// names the language; the request names it instead.
func TestRunHighlightsUnfencedCode(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.install("qwen3.5-0.8b")
	t.Setenv("MOTE_FAKE_REPLY", "def is_palindrome(s):\n    return s == s[::-1]\n")
	t.Setenv("CLICOLOR_FORCE", "1")
	code, out, coloured := e.moteTTY("run", "code", "Python function that checks for palindromes")
	if code != 0 {
		t.Fatalf("code: %d", code)
	}
	if !coloured {
		t.Skip("this platform does not colour a plain file handle")
	}
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("unfenced Python was not highlighted: %q", out)
	}
	if got := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(out, ""); !strings.Contains(got, "def is_palindrome(s):") {
		t.Errorf("highlighting changed the text: %q", got)
	}
}
