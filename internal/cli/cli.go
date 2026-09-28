// Package cli implements the mote command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/jgalego/mote/internal/bench"
	"github.com/jgalego/mote/internal/config"
	"github.com/jgalego/mote/internal/platform"
	mrt "github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/internal/task"
	"github.com/jgalego/mote/internal/ui"
	"github.com/jgalego/mote/registry"
)

// Version is set at build time with -ldflags "-X .../cli.Version=v1.2.3".
var Version = "dev"

// version describes this build. Released binaries carry the tag above;
// one installed from source (`go install ...@main`) has none, so fall back
// to the module version and commit the go tool records in the binary, which
// is what tells two source builds apart.
func version() string {
	if Version != "dev" {
		return Version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return Version
	}
	// go install ...@main records a pseudo-version, which already names the
	// commit; a plain `go build` in a checkout records only the revision.
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	for _, set := range bi.Settings {
		if set.Key == "vcs.revision" && len(set.Value) >= 7 {
			return Version + "+" + set.Value[:7]
		}
	}
	return Version
}

// Exit codes.
const (
	ExitOK      = 0
	ExitError   = 1
	ExitUsage   = 2
	ExitMissing = 3 // not set up, or a model/runtime/tool is missing
)

const usage = `mote - local, CPU-only X-to-Y AI tasks

Usage:
  mote setup [--yes] [--config FILE] [--profile P] [--data-dir DIR] [--auto-download] [--no-download]
  mote run TASK [ARGS...] [-o OUTPUT] [--apply] [--model ID] [--profile P]
  mote chat [--system "INSTRUCTIONS"] [--model ID]
  mote index [DIR...] | status | rm DIR|--all
  mote ask "QUESTION" [--in DIR] [--top N] [--sources]
  mote pipe "TASK ARGS | TASK ARGS | sh: COMMAND" [-o OUTPUT] [--trace] [--model ID]
  mote do "REQUEST" [-o OUTPUT] [--apply] [--dry-run] [--plan [--trace]] [--yes] [--model ID] [--profile P]
  mote agent "GOAL" [-o OUTPUT] [--tools a,b] [--mcp SERVER,...] [--steps N] [--allow-sh [--sandbox auto|on|off] [--sandbox-net]] [--yes] [--model ID]
  mote mcp [tools [NAME...]]
  mote listen [TASK] [--wake PHRASE] [--device D] [--chunk SECONDS] [--once]
  mote remember "FACT" | mote forget N|--all|--history | mote memory [search "Q"]
  mote serve [--port N] [--keep-alive DURATION] | status | stop
  mote tasks
  mote models [list | pull ID|CAP...|--all|--missing | upgrade [--check] [--prune] | rm ID | why CAP | verify]
  mote bench [--full] [--model ID] [--strict]
  mote tune [--apply]
  mote doctor
  mote config [show | path | set KEY VALUE | history | rollback [N] | edit]
  mote update [--check]
  mote version

Examples:
  mote run chat "Explain what a mutex is in two sentences"
  mote run code "Python function that parses ISO 8601 dates" -o dates.py
  mote run describe photo.jpg "What is written on the sign?"
  mote run transcribe meeting.m4a -o meeting.txt
  mote run speak "Build finished" -o done.wav
  mote run video clip.mp4
  mote run patch ./src "Rename the function load to read_config" --apply
  mote pipe "transcribe meeting.m4a | chat 'Summarise in 3 bullets: {}'"
  mote pipe "frames clip.mp4 3 | describe | !tee notes.txt"
  mote listen --wake "hey mote"
  mote do "summarise meeting.m4a in three bullets"
  mote agent "how many Go files are in this repository?" --allow-sh
  mote remember "I write Go, and prefer short answers"
  mote run chat "and in Python?" --continue
`

// usageError marks bad invocations (exit code 2).
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// missingError marks missing setup or dependencies (exit code 3).
type missingError struct{ msg string }

func (e missingError) Error() string { return e.msg }

func missingf(format string, a ...any) error { return missingError{fmt.Sprintf(format, a...)} }

type app struct {
	in       io.Reader
	out, err io.Writer
	cfgDir   string
	cfg      config.Config
	cfgErr   error
	reg      *registry.Registry
	info     *platform.Info
	fetcher  mrt.Fetcher
	ue, uo   *ui.UI // decorated stderr and stdout
	regURL   string
	tty      bool
	// serveURL caches serverURL for the command; inServe marks the
	// process that is the resident server, which loads models itself.
	serveURL *string
	inServe  bool
}

// Main runs the CLI and returns the process exit code.
func Main(args []string, in io.Reader, out, errw io.Writer) int {
	a := &app{in: in, out: out, err: errw, cfgDir: config.Dir(),
		regURL: "https://raw.githubusercontent.com/jgalego/mote/main/registry/models.json"}
	if u := os.Getenv("MOTE_REGISTRY_URL"); u != "" {
		a.regURL = u
	}
	if f, ok := in.(*os.File); ok {
		a.tty = ui.IsTerminal(f)
	}
	// MOTE_FORCE_LIVE lets the test suite walk the interactive setup
	// wizard, which otherwise needs a terminal on stdin.
	if os.Getenv("MOTE_FORCE_LIVE") == "1" {
		a.tty = true
	}
	a.ue, a.uo = ui.New(errw), ui.New(out)
	a.fetcher = mrt.Fetcher{Progress: barProgress{a.ue}}
	a.cfg, a.cfgErr = config.Load(a.cfgDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := a.dispatch(ctx, args)
	if err == nil {
		return ExitOK
	}
	if a.ue.Color() {
		fmt.Fprintf(errw, "%s %s\n", a.ue.Fail(), err)
	} else {
		fmt.Fprintln(errw, "mote:", err)
	}
	var ue usageError
	var me missingError
	switch {
	case errors.As(err, &ue), errors.Is(err, task.ErrUsage):
		return ExitUsage
	case errors.As(err, &me), errors.Is(err, config.ErrNotConfigured), errors.Is(err, mrt.ErrNoRuntime):
		return ExitMissing
	}
	return ExitError
}

func (a *app) dispatch(ctx context.Context, args []string) error {
	if len(args) == 0 {
		a.usage()
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		a.usage()
		return nil
	case "version", "--version":
		fmt.Fprintf(a.out, "mote %s (%s/%s, registry %s)\n", version(), runtime.GOOS, runtime.GOARCH, a.registry().Version)
		return nil
	case "setup":
		return a.setup(ctx, rest)
	case "config":
		return a.config(rest)
	case "doctor":
		return a.doctor()
	case "tasks":
		return a.tasks()
	case "mcp":
		return a.mcpCmd(ctx, rest)
	case "update":
		return a.update(ctx, rest)
	}
	switch cmd {
	case "run", "pipe", "listen", "do", "agent", "memory", "remember", "forget", "models", "bench", "tune", "serve", "chat", "index", "ask":
	default:
		return usagef("unknown command %q; see `mote help`", cmd)
	}
	// Remaining commands need a valid configuration.
	if a.cfgErr != nil {
		return a.cfgErr
	}
	switch cmd {
	case "run":
		return a.run(ctx, rest)
	case "pipe":
		return a.pipe(ctx, rest)
	case "listen":
		return a.listen(ctx, rest)
	case "do":
		return a.do(ctx, rest)
	case "agent":
		return a.agent(ctx, rest)
	case "remember":
		return a.remember(rest)
	case "forget":
		return a.forget(rest)
	case "memory":
		return a.memoryCmd(ctx, rest)
	case "models":
		return a.models(ctx, rest)
	case "bench":
		return a.bench(ctx, rest)
	case "tune":
		return a.tune(rest)
	case "serve":
		return a.serveCmd(ctx, rest)
	case "chat":
		return a.chatCmd(ctx, rest)
	case "index":
		return a.indexCmd(ctx, rest)
	case "ask":
		return a.askCmd(ctx, rest)
	}
	return usagef("unknown command %q; see `mote help`", cmd)
}

// flags separates known flags from positional arguments. Flags may appear
// anywhere; "--" ends flag parsing.
func flags(args []string, withValue, boolean []string) (map[string]string, []string, error) {
	vals := map[string]string{}
	var pos []string
	is := func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		name, val, hasVal := strings.Cut(arg, "=")
		switch {
		case is(withValue, name):
			if !hasVal {
				if i+1 >= len(args) {
					return nil, nil, usagef("%s needs a value", name)
				}
				i++
				val = args[i]
			}
			vals[name] = val
		case is(boolean, name) && !hasVal:
			vals[name] = "true"
		case strings.HasPrefix(arg, "-") && arg != "-" && len(arg) > 1:
			return nil, nil, usagef("unknown flag %s", arg)
		default:
			pos = append(pos, arg)
		}
	}
	return vals, pos, nil
}

func (a *app) platform() platform.Info {
	if a.info == nil {
		i := platform.Detect()
		a.info = &i
	}
	return *a.info
}

// registry returns the installed registry if it is valid and not older than
// the embedded one, else the embedded registry.
func (a *app) registry() *registry.Registry {
	if a.reg != nil {
		return a.reg
	}
	a.reg = registry.Default()
	if b, err := os.ReadFile(a.userRegistryPath()); err == nil {
		if r, err := registry.Parse(b); err == nil && !registry.Newer(a.reg.Version, r.Version) {
			a.reg = r
		} else if err != nil && !errors.Is(err, registry.ErrOldSchema) {
			// One from an older mote is superseded by the embedded
			// registry, which needs no warning.
			fmt.Fprintf(a.err, "warning: ignoring invalid %s: %v\n", a.userRegistryPath(), err)
		}
	}
	return a.reg
}

func (a *app) dataDir() string {
	if a.cfgErr == nil {
		return a.cfg.Data()
	}
	return config.DefaultDataDir()
}

func (a *app) userRegistryPath() string {
	return filepath.Join(a.dataDir(), "registry", "models.json")
}

func (a *app) store() mrt.Store { return mrt.Store{Dir: a.dataDir()} }

func (a *app) ramMB() int { return a.platform().RAMMB }

// choose returns the model the configuration uses for a capability.
func (a *app) choose(capability, profile string) (*registry.Model, registry.Choice, error) {
	reg := a.registry()
	if id, ok := a.cfg.Models[capability]; ok {
		m, found := reg.Model(id)
		if !found {
			return nil, registry.Choice{}, fmt.Errorf("config pins %s to unknown model %q; run `mote config set models.%s \"\"`", capability, id, capability)
		}
		if !m.Has(capability) {
			return nil, registry.Choice{}, fmt.Errorf("config pins %s to %s, which lacks that capability", capability, id)
		}
		return m, registry.Choice{Model: id, Reason: "pinned in config", Meets: true}, nil
	}
	ch, err := reg.Select(capability, profile, registry.Env{RAMMB: a.ramMB()})
	if err != nil {
		return nil, ch, missingf("%v", err)
	}
	m, _ := reg.Model(ch.Model)
	return m, ch, nil
}

// backend returns how to run a model: in the resident server when one is
// running or keep_alive lets this command start one, else in this process.
func (a *app) backend(m *registry.Model) (mrt.Backend, error) {
	b, err := a.localBackend(m)
	if l, ok := b.(*mrt.Llama); ok && err == nil {
		return a.servedBackend(l), nil
	}
	return b, err
}

// localBackend runs a model in this process, which is what measurements
// need.
func (a *app) localBackend(m *registry.Model) (mrt.Backend, error) {
	if m.Backend == "sd.cpp" {
		rt, _, err := a.runtimeFor(m)
		if err != nil {
			return nil, err
		}
		dir, ok := a.store().RuntimeInstalled(rt)
		if !ok {
			return nil, missingf("stable-diffusion.cpp is not installed; run `mote models pull %s`", m.ID)
		}
		return &mrt.SD{Dir: dir, Threads: a.cfg.Threads, LogDir: filepath.Join(a.dataDir(), "logs")}, nil
	}
	if m.Backend != "llama.cpp" {
		return nil, fmt.Errorf("%w: %s", mrt.ErrUnsupported, m.Backend)
	}
	return a.llama()
}

// runtimeFor returns the pinned runtime a model needs and its build for
// this machine, or says why there is none.
func (a *app) runtimeFor(m *registry.Model) (registry.Runtime, registry.Asset, error) {
	rt, ok := a.registry().RuntimeFor(m.Backend)
	if !ok {
		return rt, registry.Asset{}, fmt.Errorf("%w: no %s runtime is pinned", mrt.ErrUnsupported, m.Backend)
	}
	info := a.platform()
	asset, ok := rt.Asset(info.OS, info.Arch)
	if !ok {
		return rt, asset, missingf("%s runs on %s, which has no prebuilt build for %s/%s", m.ID, m.Backend, info.OS, info.Arch)
	}
	return rt, asset, nil
}

// ready reports whether a model can run without downloading anything: its
// files, and for other backends than llama.cpp its runtime.
func (a *app) ready(m *registry.Model) bool {
	if !a.store().Installed(m) {
		return false
	}
	if m.Backend == "llama.cpp" {
		return true
	}
	rt, ok := a.registry().RuntimeFor(m.Backend)
	if !ok {
		return false
	}
	_, ok = a.store().RuntimeInstalled(rt)
	return ok
}

// ensure makes sure a model's files, and the runtime of a backend other
// than llama.cpp, are present, downloading them only when the user allowed
// automatic downloads. A platform the runtime has no build for is reported
// before anything is fetched.
func (a *app) ensure(ctx context.Context, m *registry.Model, allow bool) error {
	var rt registry.Runtime
	var asset registry.Asset
	needRuntime := false
	if m.Backend != "llama.cpp" {
		var err error
		if rt, asset, err = a.runtimeFor(m); err != nil {
			return err
		}
		_, installed := a.store().RuntimeInstalled(rt)
		needRuntime = !installed
	}
	missing := a.store().Missing(m)
	if len(missing) == 0 && !needRuntime {
		return nil
	}
	var size int64
	for _, f := range missing {
		size += f.Size
	}
	if needRuntime {
		size += asset.Size
	}
	if !allow {
		if len(missing) == 0 {
			return missingf("%s needs stable-diffusion.cpp %s (%s), which is not installed; run `mote models pull %s`", m.ID, rt.Version, mb(size), m.ID)
		}
		return missingf("model %s (%s) is not downloaded; run `mote models pull %s`", m.ID, mb(size), m.ID)
	}
	if len(missing) > 0 {
		fmt.Fprintf(a.err, "%s downloading %s %s\n", a.ue.Arrow(), a.ue.Bold(m.ID), a.ue.Dim(fmt.Sprintf("(%s, %s, from %s)", mb(size), m.License, hostOf(missing[0].URL))))
		if err := a.store().Pull(ctx, a.fetcher, m); err != nil {
			return err
		}
	}
	if needRuntime {
		fmt.Fprintf(a.err, "%s installing %s %s %s\n", a.ue.Arrow(), rt.Name, rt.Version, a.ue.Dim(fmt.Sprintf("(%s, %s, from %s)", mb(asset.Size), rt.License, hostOf(asset.URL))))
		if _, err := a.store().InstallRuntime(ctx, a.fetcher, rt, asset); err != nil {
			return err
		}
	}
	return nil
}

func (a *app) tool(name string) (string, error) {
	if p, ok := a.cfg.Tools[name]; ok {
		return p, nil
	}
	if a.cfgErr == nil && !a.cfg.ReuseTools {
		return "", missingf("%s is needed but reuse_tools is off; set tools.%s to its path", name, name)
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", missingf("%s is needed for this task but was not found on PATH; %s", name, installHint(name))
}

func installHint(tool string) string { return installHintOn(tool, runtime.GOOS) }

// installHintOn takes the operating system as an argument so the advice for
// every platform can be checked from any of them.
func installHintOn(tool, goos string) string {
	// Packages named differently from the program they install. Tools
	// wrapped by exec tasks can be anything, so an unknown one gets general
	// advice rather than a guessed package name.
	pkg, known := map[string]string{"ffmpeg": "ffmpeg", "ffprobe": "ffmpeg", "git": "git", "rg": "ripgrep", "jq": "jq",
		"pdftotext": "poppler-utils", "pdftoppm": "poppler-utils"}[tool]
	general := "install " + tool + " with your package manager, or set tools." + tool + " to its path"
	if !known {
		return general
	}
	switch goos {
	case "darwin":
		if pkg == "poppler-utils" {
			pkg = "poppler" // Homebrew's name for it
		}
		return "install it with `brew install " + pkg + "`"
	case "windows":
		id, ok := map[string]string{
			"ffmpeg": "Gyan.FFmpeg", "git": "Git.Git", "ripgrep": "BurntSushi.ripgrep.MSVC", "jq": "jqlang.jq"}[pkg]
		if !ok {
			return general
		}
		return "install it with `winget install " + id + "`"
	}
	return "install it with your package manager, e.g. `sudo apt install " + pkg + "`"
}

func mb(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%d MB", n>>20)
}

func hostOf(u string) string {
	u = strings.TrimPrefix(u, "https://")
	h, _, _ := strings.Cut(u, "/")
	return h
}

func (a *app) run(ctx context.Context, args []string) error {
	vals, pos, err := flags(args, []string{"-o", "--output", "--model", "--profile"},
		[]string{"--apply", "--continue", "--recall"})
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usagef("which task? see `mote tasks`")
	}
	tasks, err := task.LoadFrom(a.tasksDir())
	if err != nil {
		return err
	}
	t, ok := task.Find(tasks, pos[0])
	if !ok {
		return usagef("unknown task %q; see `mote tasks`", pos[0])
	}
	profile, err := a.selectModels(vals)
	if err != nil {
		return err
	}
	out := firstNonEmpty(vals["-o"], vals["--output"])
	sessions := map[string]mrt.Session{}
	defer task.CloseSessions(sessions)
	env := a.env(profile, sessions)
	request := strings.Join(pos[1:], " ")
	if env.Memory, err = a.memoryFor(ctx, request, vals, profile, sessions); err != nil {
		return err
	}
	// Highlight source code and JSON, but only on the way to a terminal:
	// files and pipes keep the exact bytes the model produced.
	var code *ui.CodeStream
	hint := langHint(pos[1:])
	if out == "" && (t.Out == "code" || t.Out == "data") {
		code = a.uo.CodeStream(highlightLang(t, hint))
	}
	// Stream to interactive terminals only: pipes get the post-processed
	// value (fences stripped, JSON normalised) in one piece.
	if out == "" && a.uo.Live() {
		if code != nil {
			env.Stream, env.Lang = code.Write, code.Lang
		} else {
			env.Stream = func(tok string) { fmt.Fprint(a.out, tok) }
		}
	}
	defer os.RemoveAll(env.TempDir)
	res, err := t.Run(ctx, env, pos[1:], task.Options{Output: out, Apply: vals["--apply"] == "true"})
	if err != nil {
		return err
	}
	if res.Lang == "" {
		res.Lang = hint
	}
	a.record(t, request, res)
	return a.emit(t, res, out, code)
}

// selectModels applies --profile and --model, returning the profile to
// resolve capabilities with.
func (a *app) selectModels(vals map[string]string) (string, error) {
	profile := a.cfg.Profile
	if p := vals["--profile"]; p != "" {
		profile = p
	}
	id := vals["--model"]
	if id == "" {
		return profile, nil
	}
	m, ok := a.registry().Model(id)
	if !ok {
		return "", usagef("unknown model %q; see `mote models`", id)
	}
	// --model overrides every capability this model provides.
	models := map[string]string{}
	for k, v := range a.cfg.Models {
		models[k] = v
	}
	for _, c := range m.Caps {
		models[c] = id
	}
	a.cfg.Models = models
	return profile, nil
}

// env builds the task environment. Sessions, when given, is a cache shared
// by the stages of a pipeline so models stay loaded between them; the caller
// closes it and removes TempDir.
func (a *app) env(profile string, sessions map[string]mrt.Session) task.Env {
	return task.Env{
		Resolve: func(ctx context.Context, c string) (*registry.Model, map[string]string, error) {
			m, _, err := a.choose(c, profile)
			if err != nil {
				return nil, nil, err
			}
			if err := a.ensure(ctx, m, a.cfg.AutoDownload); err != nil {
				return nil, nil, err
			}
			return m, a.store().Files(m), nil
		},
		Backend:  a.backend,
		Tool:     a.tool,
		Stdin:    a.in,
		Log:      a.err,
		TempDir:  filepath.Join(a.dataDir(), "tmp"),
		Status:   a.status,
		Sessions: sessions,
	}
}

// emit writes a finished result: file paths, a written file, or the text,
// highlighted when code was streaming it to a terminal.
func (a *app) emit(t task.Task, res task.Result, out string, code *ui.CodeStream) error {
	switch {
	case len(res.Files) > 0:
		for _, f := range res.Files {
			fmt.Fprintln(a.out, f)
		}
	case out != "":
		if err := os.WriteFile(out, []byte(strings.TrimRight(res.Text, "\n")+"\n"), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(a.err, "%s wrote %s\n", a.ue.OK(), out)
	case res.Streamed:
		code.Flush()
		fmt.Fprintln(a.out)
	case code != nil:
		fmt.Fprintln(a.out, a.uo.Highlight(strings.TrimRight(res.Text, "\n"), highlightLang(t, res.Lang)))
	case t.Out == "diff" && a.uo.Color():
		for _, l := range strings.Split(strings.TrimRight(res.Text, "\n"), "\n") {
			switch {
			case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
				l = a.uo.Bold(l)
			case strings.HasPrefix(l, "+"):
				l = a.uo.Green(l)
			case strings.HasPrefix(l, "-"):
				l = a.uo.Red(l)
			case strings.HasPrefix(l, "@@"):
				l = a.uo.Cyan(l)
			}
			fmt.Fprintln(a.out, l)
		}
	default:
		fmt.Fprintln(a.out, strings.TrimRight(res.Text, "\n"))
	}
	a.footer(res.Calls)
	return nil
}

// highlightLang maps a task and the language its reply named to a lexer
// name. A task that always produces one format wins over the model's fence,
// which is often absent or wrong. Diffs keep their own colouring below.
func highlightLang(t task.Task, fence string) string {
	if t.Out == "data" {
		return "json"
	}
	return fence
}

// promptLangs are language names a request may mention, mapped to lexer
// names. Only unambiguous words count: "go" is also a verb, so Go needs its
// capital or "golang", and one-letter names like C and R are left out.
var promptLangs = map[string]string{
	"python": "python", "javascript": "javascript", "typescript": "typescript", "rust": "rust",
	"java": "java", "kotlin": "kotlin", "swift": "swift", "ruby": "ruby", "php": "php",
	"bash": "bash", "shell": "bash", "zsh": "bash", "powershell": "powershell", "sql": "sql",
	"html": "html", "css": "css", "lua": "lua", "perl": "perl", "haskell": "haskell",
	"scala": "scala", "c++": "cpp", "cpp": "cpp", "c#": "csharp", "csharp": "csharp",
	"golang": "go", "yaml": "yaml", "json": "json", "dockerfile": "docker", "makefile": "make",
	"elixir": "elixir", "dart": "dart", "julia": "julia", "zig": "zig",
}

// langHint guesses the language of code a task will produce from its
// arguments: a file's extension, else a language the request names. It is
// used when the reply has no fence to say, which is how small models told to
// answer with code only often reply, and short code defeats detection by
// content alone.
func langHint(args []string) string {
	for _, a := range args {
		if st, err := os.Stat(a); err == nil && !st.IsDir() {
			if l := ui.LangForFile(a); l != "" {
				return l
			}
		}
	}
	for _, a := range args {
		for _, w := range strings.FieldsFunc(a, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '+' || r == '#')
		}) {
			if w == "Go" {
				return "go"
			}
			if l, ok := promptLangs[strings.ToLower(w)]; ok {
				return l
			}
		}
	}
	return ""
}

// footer prints a one-line summary of model usage on interactive stderr.
func (a *app) footer(calls []task.Call) {
	if !a.ue.Live() || len(calls) == 0 {
		return
	}
	models := map[string]bool{}
	var names []string
	var tokens int
	var ms float64
	for _, c := range calls {
		if !models[c.Model] {
			models[c.Model] = true
			names = append(names, c.Model)
		}
		tokens += c.Result.OutputTokens
		ms += c.Result.GenMS
	}
	line := fmt.Sprintf("%s · %d tokens", strings.Join(names, " + "), tokens)
	if ms > 0 {
		line += fmt.Sprintf(" · %.1f tok/s", float64(tokens)/ms*1000)
	}
	fmt.Fprintln(a.err, a.ue.Dim(line+" · local CPU"))
}

// tasksDir is where user-defined tasks live: *.json files shaped like the
// built-in tasks.json, loaded on top of it.
func (a *app) tasksDir() string {
	if d := os.Getenv("MOTE_TASKS_DIR"); d != "" {
		return d
	}
	return filepath.Join(a.cfgDir, "tasks")
}

func (a *app) tasks() error {
	tasks, err := task.LoadFrom(a.tasksDir())
	if err != nil {
		return err
	}
	for _, t := range tasks {
		o := a.uo
		summary := t.Summary
		if t.Custom() {
			summary += o.Dim(" · custom")
		}
		fmt.Fprintf(a.out, "%s %s %s\n", o.Bold(pad(t.ID, 11)), o.Accent(pad(strings.Join(t.In, "+")+" -> "+t.Out, 22)), summary)
		fmt.Fprintf(a.out, "%-11s %s\n", "", o.Dim("usage: mote run "+t.ID+" "+t.Usage()))
		var needs []string
		for _, c := range t.Caps() {
			status := "not set up"
			if a.cfgErr == nil {
				if m, _, err := a.choose(c, a.cfg.Profile); err != nil {
					status = a.uo.Red("no model fits")
				} else if a.store().Installed(m) {
					status = a.uo.Green(m.ID)
				} else {
					status = a.uo.Yellow(m.ID + ", not downloaded")
				}
			}
			needs = append(needs, c+": "+status)
		}
		for _, tl := range t.Tools() {
			if _, err := a.tool(tl); err == nil {
				needs = append(needs, tl+": "+a.uo.Green("ok"))
			} else {
				needs = append(needs, tl+": "+a.uo.Yellow("missing"))
			}
		}
		fmt.Fprintf(a.out, "%-11s %s %s\n", "", a.uo.Dim("needs:"), strings.Join(needs, "; "))
	}
	fmt.Fprintf(a.out, "%s\n", a.uo.Dim("your own tasks: "+filepath.Join(a.tasksDir(), "*.json")+" (see docs/extending.md)"))
	return nil
}

func (a *app) models(ctx context.Context, args []string) error {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	reg := a.registry()
	switch sub {
	case "list", "ls":
		used := map[string][]string{}
		var caps []string
		for c := range registry.Capabilities {
			caps = append(caps, c)
		}
		sort.Strings(caps)
		for _, c := range caps {
			if m, _, err := a.choose(c, a.cfg.Profile); err == nil {
				used[m.ID] = append(used[m.ID], c)
			}
		}
		o := a.uo
		fmt.Fprintln(a.out, o.Bold(fmt.Sprintf("%-16s %-8s %-7s %-9s %-9s %-26s %s", "MODEL", "QUANT", "PARAMS", "DOWNLOAD", "RAM(EST)", "CAPABILITIES", "STATUS")))
		for i := range reg.Models {
			m := &reg.Models[i]
			status := o.Dim("-")
			if a.store().Installed(m) {
				status = o.Green("installed")
			}
			if u := used[m.ID]; len(u) > 0 {
				status += ", " + o.Cyan("default for "+strings.Join(u, ","))
			}
			fmt.Fprintf(a.out, "%s %-8s %-7s %-9s %-9s %s %s\n", o.Bold(pad(m.ID, 16)), m.Quant, fmt.Sprintf("%.1fB", m.ParamsB),
				mb(m.Bytes()), fmt.Sprintf("%d MB", m.RAMEstimate), o.Accent(pad(strings.Join(m.Caps, ","), 26)), status)
		}
		fmt.Fprintln(a.out, o.Dim(fmt.Sprintf("\nregistry %s · profile %s · runtime llama.cpp %s", reg.Version, a.cfg.Profile, reg.Runtime.Version)))
		return nil
	case "pull":
		vals, pos, err := flags(args, nil, []string{"--all", "--missing", "--yes", "-y"})
		if err != nil {
			return err
		}
		all, missing := vals["--all"] == "true", vals["--missing"] == "true"
		if (len(pos) == 0) == !(all || missing) || (all && missing) {
			return usagef("usage: mote models pull ID|CAPABILITY... | --all | --missing [--yes]")
		}
		yes := vals["--yes"] == "true" || vals["-y"] == "true"
		if all {
			return a.pullMany(ctx, a.pullable(), nil, yes)
		}
		if missing {
			ms, _ := a.wanted()
			return a.pullMany(ctx, ms, nil, yes)
		}
		for _, name := range pos {
			m, ok := reg.Model(name)
			if !ok {
				if _, isCap := registry.Capabilities[name]; !isCap {
					return usagef("unknown model or capability %q", name)
				}
				var err error
				if m, _, err = a.choose(name, a.cfg.Profile); err != nil {
					return err
				}
			}
			if a.ready(m) {
				fmt.Fprintf(a.out, "%s %s already installed\n", a.uo.OK(), m.ID)
				continue
			}
			if err := a.ensure(ctx, m, true); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "%s %s installed %s\n", a.uo.OK(), a.uo.Bold(m.ID), a.uo.Dim("(sha256 verified)"))
		}
		return nil
	case "upgrade":
		return a.upgrade(ctx, args)
	case "rm", "remove":
		if len(args) != 1 {
			return usagef("usage: mote models rm ID")
		}
		m, ok := reg.Model(args[0])
		if !ok {
			return usagef("unknown model %q", args[0])
		}
		return a.store().Remove(m)
	case "verify":
		bad := 0
		for i := range reg.Models {
			m := &reg.Models[i]
			if !a.store().Installed(m) {
				continue
			}
			if err := a.store().Verify(m); err != nil {
				bad++
				fmt.Fprintf(a.out, "%s: %v\n", m.ID, err)
			} else {
				fmt.Fprintf(a.out, "%s: ok\n", m.ID)
			}
		}
		if bad > 0 {
			return fmt.Errorf("%d models failed verification; re-download with `mote models rm ID && mote models pull ID`", bad)
		}
		return nil
	case "why":
		if len(args) != 1 {
			return usagef("usage: mote models why CAPABILITY")
		}
		c := args[0]
		if _, ok := registry.Capabilities[c]; !ok {
			return usagef("unknown capability %q (have %s)", c, strings.Join(capNames(), ", "))
		}
		m, ch, err := a.choose(c, a.cfg.Profile)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s %s %s %s\n%s\n", a.uo.Accent(c), a.uo.Arrow(), a.uo.Bold(m.ID), a.uo.Dim(fmt.Sprintf("(profile %s, %d MB RAM detected)", a.cfg.Profile, a.ramMB())), ch.Reason)
		if g, ok := reg.Policy.Gates[c]; ok && g.Note != "" {
			fmt.Fprintln(a.out, "note:", g.Note)
		}
		for _, b := range m.Benchmarks {
			v := ""
			if b.Verified {
				v = ", verified"
			}
			fmt.Fprintf(a.out, "  %s %s %s = %g (%s, %s%s) %s\n", b.Kind, b.Dataset, b.Task, b.Value, b.Source, b.Date, v, b.URL)
		}
		if res, err := bench.Load(a.dataDir()); err == nil {
			if e, ok := res.Entries[m.ID]; ok {
				fmt.Fprintf(a.out, "  local %s: %.1f tok/s, %d MB peak, startup %.0f ms, %d/%d checks (%s)\n",
					e.Mode, e.TokensPerSec, e.PeakRSSMB, e.StartupMS, e.Passed, e.Cases, e.Time.Format("2006-01-02"))
			}
		}
		return nil
	}
	return usagef("unknown models subcommand %q", sub)
}

func capNames() []string {
	var out []string
	for c := range registry.Capabilities {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
