// Package task runs X-to-Y pipelines. A task is a short list of steps; each
// step is an op (a model call or a local tool) that reads named values and
// writes one. Tasks are data (tasks.json), ops are Go functions, so a new task
// usually needs no code and a new tool or modality needs one op.
package task

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/registry"
)

//go:embed tasks.json
var embedded []byte

type Task struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
	// Examples are things someone might ask for. They are what `mote do`
	// matches a request against, so a task is found by what it is for
	// rather than by its name.
	Examples []string `json:"examples,omitempty"`
	In       []string `json:"in"`
	Out      string   `json:"out"`
	Params   []Param  `json:"params"`
	Output   string   `json:"output,omitempty"` // default output path, "required", or empty for stdout
	// Asks marks a task that can change things, such as one wrapping a
	// program that writes or deletes. When a model chose it (mote agent,
	// mote do), mote confirms before running it.
	Asks  bool   `json:"asks,omitempty"`
	Steps []Step `json:"steps"`

	// Source is the file a user-defined task came from, empty for built-ins.
	Source string `json:"-"`
}

// TakesText reports whether a parameter accepts text typed or piped in,
// rather than only a path.
func (p Param) TakesText() bool { return p.Kind == "text" || p.Kind == "input" }

// Custom reports whether t was defined by the user rather than built in.
func (t Task) Custom() bool { return t.Source != "" }

type Param struct {
	Name     string  `json:"name"`
	Kind     string  `json:"kind"` // text, file, dir, input (a file's path or the text itself)
	Optional bool    `json:"optional,omitempty"`
	Default  *string `json:"default,omitempty"`
}

type Step struct {
	Op         string `json:"op"`
	Cap        string `json:"cap,omitempty"`
	From       string `json:"from,omitempty"`
	As         string `json:"as"`
	System     string `json:"system,omitempty"`
	Prompt     string `json:"prompt,omitempty"`
	Images     string `json:"images,omitempty"`
	Audio      string `json:"audio,omitempty"`
	JSONSchema string `json:"json_schema,omitempty"`
	Each       bool   `json:"each,omitempty"`
	Fences     string `json:"fences,omitempty"`
	Count      string `json:"count,omitempty"`
	Width      string `json:"width,omitempty"`
	Dir        string `json:"dir,omitempty"`
	Optional   bool   `json:"optional,omitempty"`
	Seed       string `json:"seed,omitempty"`
	// Plain makes a read step reduce HTML to the text a reader sees. PDF
	// and office documents are always reduced to their text.
	Plain bool `json:"plain,omitempty"`
	// Split names a value that may be too long for the model's context.
	// When it is, the step runs once per chunk of it, and Reduce, a prompt
	// in which the same name stands for the partial results, combines them.
	// Without Reduce the partial results are joined in order.
	Split  string `json:"split,omitempty"`
	Reduce string `json:"reduce,omitempty"`
	// Empty is the error to give when the step produces nothing, such as
	// a diff of no changes, instead of passing nothing to a model.
	Empty string `json:"empty,omitempty"`
	// Cmd is the argument vector of an exec step: a program on PATH, then
	// its arguments, which may use {{name}} like a prompt. It never goes
	// through a shell.
	Cmd []string `json:"cmd,omitempty"`
}

// Value is what flows between steps: text, files, or both.
type Value struct {
	Text  string
	Files []string
}

func (v Value) empty() bool { return v.Text == "" && len(v.Files) == 0 }

// Env connects a run to models, backends and local tools.
type Env struct {
	// Resolve returns the model for a capability with its local file paths,
	// downloading it first if the user allowed that.
	Resolve func(ctx context.Context, capability string) (*registry.Model, map[string]string, error)
	Backend func(m *registry.Model) (runtime.Backend, error)
	Tool    func(name string) (string, error)
	Stdin   io.Reader
	Log     io.Writer
	TempDir string
	// Status, when set, is called when slow work starts; the returned func
	// is called with the outcome when it ends. The CLI shows spinners.
	Status func(msg string) func(ok bool)
	// Stream, when set, receives the final answer as it is generated, for
	// steps whose output needs no post-processing.
	Stream func(token string)
	// Lang, when set, is called with the language named by a Markdown fence
	// in a streamed reply ("python"), before the code itself is streamed.
	// The CLI uses it to highlight what it prints.
	Lang func(lang string)
	// Memory, when set, is put in front of the system prompt of every
	// model step: facts the user asked mote to remember, and exchanges
	// recalled from its history.
	Memory string
	// Sessions, when set, holds open model sessions across runs so the
	// stages of a pipeline keep their models loaded. Whoever supplies it
	// owns it and must call CloseSessions; a run with no cache of its own
	// closes the sessions it opened.
	Sessions map[string]runtime.Session
}

// CloseSessions shuts down the sessions in a cache shared between runs.
func CloseSessions(sessions map[string]runtime.Session) {
	for id, s := range sessions {
		s.Close()
		delete(sessions, id)
	}
}

// Options are per-run switches from the command line.
type Options struct {
	Output string
	Apply  bool
}

// Result is the final value plus model usage.
type Result struct {
	Value
	Calls    []Call
	Streamed bool   // the text was already written through Env.Stream
	Lang     string // language named by the reply's Markdown fence, if any
}

// Call records one model invocation, for reporting and benchmarks.
type Call struct {
	Model  string
	Cap    string
	Result runtime.Result
}

// Load returns the built-in tasks.
func Load() ([]Task, error) { return Parse(embedded) }

// LoadFrom returns the built-in tasks merged with the user's own, read from
// the *.json files in dir (each shaped like tasks.json). A user task with the
// id of a built-in replaces it, so a prompt can be adjusted without forking
// mote; two user files defining the same id is an error, as is a file that
// does not parse or validate. A missing dir simply means no custom tasks.
func LoadFrom(dir string) ([]Task, error) {
	tasks, err := Load()
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return tasks, nil
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	byID := map[string]int{}
	for i, t := range tasks {
		byID[t.ID] = i
	}
	from := map[string]string{} // id -> the user file that defined it
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		custom, err := Parse(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		for _, t := range custom {
			if prev, ok := from[t.ID]; ok {
				return nil, fmt.Errorf("%s: task %q is already defined in %s", f, t.ID, prev)
			}
			from[t.ID] = f
			t.Source = f
			if i, ok := byID[t.ID]; ok {
				tasks[i] = t
				continue
			}
			byID[t.ID] = len(tasks)
			tasks = append(tasks, t)
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}

// Parse decodes and validates task definitions.
func Parse(b []byte) ([]Task, error) {
	var doc struct {
		Tasks []Task `json:"tasks"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("tasks: %w", err)
	}
	seen := map[string]bool{}
	for _, t := range doc.Tasks {
		if seen[t.ID] {
			return nil, fmt.Errorf("tasks: duplicate id %q", t.ID)
		}
		seen[t.ID] = true
		if err := t.validate(); err != nil {
			return nil, fmt.Errorf("task %s: %w", t.ID, err)
		}
	}
	sort.Slice(doc.Tasks, func(i, j int) bool { return doc.Tasks[i].ID < doc.Tasks[j].ID })
	return doc.Tasks, nil
}

// Find returns the task with the given id.
func Find(tasks []Task, id string) (Task, bool) {
	for _, t := range tasks {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}

var tmplRe = regexp.MustCompile(`\{\{([a-z_]+)\}\}`)

func (t Task) validate() error {
	defined := map[string]bool{}
	for _, p := range t.Params {
		if p.Kind != "text" && p.Kind != "file" && p.Kind != "dir" && p.Kind != "input" {
			return fmt.Errorf("param %s: unknown kind %q", p.Name, p.Kind)
		}
		if defined[p.Name] {
			return fmt.Errorf("param %s declared twice", p.Name)
		}
		defined[p.Name] = true
	}
	if len(t.Steps) == 0 {
		return errors.New("no steps")
	}
	for i, s := range t.Steps {
		op, ok := ops[s.Op]
		if !ok {
			return fmt.Errorf("step %d: unknown op %q", i+1, s.Op)
		}
		if op.model {
			if _, ok := registry.Capabilities[s.Cap]; !ok {
				return fmt.Errorf("step %d: op %s needs a known capability, got %q", i+1, s.Op, s.Cap)
			}
		}
		for _, ref := range []string{s.From, s.Images, s.Audio, s.JSONSchema, s.Dir, s.Seed, s.Split} {
			if ref != "" && !defined[ref] {
				return fmt.Errorf("step %d: %q is not defined before use", i+1, ref)
			}
		}
		if err := s.validateCmd(); err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
		if s.Reduce != "" && s.Split == "" {
			return fmt.Errorf("step %d: reduce needs split", i+1)
		}
		if s.Split != "" && !strings.Contains(s.Prompt, "{{"+s.Split+"}}") {
			return fmt.Errorf("step %d: split value %q is not used in the prompt", i+1, s.Split)
		}
		for _, m := range tmplRe.FindAllStringSubmatch(s.Prompt+s.System+s.Reduce+strings.Join(s.Cmd, " "), -1) {
			if !defined[m[1]] {
				return fmt.Errorf("step %d: template references undefined %q", i+1, m[1])
			}
		}
		if s.As == "" {
			return fmt.Errorf("step %d: missing \"as\"", i+1)
		}
		defined[s.As] = true
	}
	if !defined["out"] {
		return errors.New(`no step produces "out"`)
	}
	return nil
}

// Usage returns the positional argument synopsis.
func (t Task) Usage() string {
	var parts []string
	for _, p := range t.Params {
		n := strings.ToUpper(p.Name)
		if p.Optional || p.Default != nil {
			n = "[" + n + "]"
		}
		parts = append(parts, n)
	}
	return strings.Join(parts, " ")
}

// Caps lists the model capabilities a task needs.
func (t Task) Caps() []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range t.Steps {
		if ops[s.Op].model && !seen[s.Cap] {
			seen[s.Cap] = true
			out = append(out, s.Cap)
		}
	}
	return out
}

// Tools lists the local tools a task needs.
func (t Task) Tools() []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range t.Steps {
		tools := ops[s.Op].tools
		if s.Op == "exec" && len(s.Cmd) > 0 {
			tools = []string{s.Cmd[0]}
		}
		for _, tool := range tools {
			if !seen[tool] {
				seen[tool] = true
				out = append(out, tool)
			}
		}
	}
	return out
}

// ErrUsage marks errors in how a task was invoked.
var ErrUsage = errors.New("usage")

type run struct {
	ctx      context.Context
	env      Env
	opt      Options
	task     Task
	vars     map[string]Value
	sessions map[string]runtime.Session
	calls    []Call
	streamed bool
	lang     string
}

// Run executes t with positional args.
func (t Task) Run(ctx context.Context, env Env, args []string, opt Options) (Result, error) {
	r := &run{ctx: ctx, env: env, opt: opt, task: t, vars: map[string]Value{}, sessions: env.Sessions}
	if r.sessions == nil {
		r.sessions = map[string]runtime.Session{}
		defer r.close()
	}
	if t.Output == "required" && opt.Output == "" {
		return Result{}, fmt.Errorf("%w: task %s needs -o OUTPUT", ErrUsage, t.ID)
	}
	if len(args) > len(t.Params) {
		return Result{}, fmt.Errorf("%w: too many arguments; usage: mote run %s %s", ErrUsage, t.ID, t.Usage())
	}
	for i, p := range t.Params {
		var v string
		switch {
		case i < len(args):
			v = args[i]
		case p.Default != nil:
			v = *p.Default
		case p.Optional:
			continue
		default:
			return Result{}, fmt.Errorf("%w: missing %s; usage: mote run %s %s", ErrUsage, strings.ToUpper(p.Name), t.ID, t.Usage())
		}
		val, err := r.param(p, v)
		if err != nil {
			return Result{}, err
		}
		r.vars[p.Name] = val
	}
	for i, s := range t.Steps {
		v, err := ops[s.Op].fn(r, s)
		if err != nil {
			if s.Optional {
				r.logf("skipped optional step %d (%s): %v", i+1, s.Op, err)
				r.vars[s.As] = Value{}
				continue
			}
			return Result{Calls: r.calls}, fmt.Errorf("%s step %d (%s): %w", t.ID, i+1, s.Op, err)
		}
		if s.Empty != "" && strings.TrimSpace(v.Text) == "" && len(v.Files) == 0 {
			return Result{Calls: r.calls}, fmt.Errorf("%s: %s", t.ID, s.Empty)
		}
		r.vars[s.As] = v
	}
	return Result{Value: r.vars["out"], Calls: r.calls, Streamed: r.streamed, Lang: r.lang}, nil
}

func (r *run) param(p Param, v string) (Value, error) {
	switch p.Kind {
	case "input":
		// A path to a file stands for its contents, which a read step
		// extracts; anything else is the text itself.
		if st, err := os.Stat(v); err == nil && st.Mode().IsRegular() {
			return Value{Text: v, Files: []string{v}}, nil
		}
		fallthrough
	case "text":
		if v == "-" && r.env.Stdin != nil {
			b, err := io.ReadAll(io.LimitReader(r.env.Stdin, maxRead))
			if err != nil {
				return Value{}, err
			}
			v = string(b)
		}
		return Value{Text: v}, nil
	case "file", "dir":
		st, err := os.Stat(v)
		if err != nil {
			return Value{}, fmt.Errorf("%w: %s: %v", ErrUsage, p.Name, err)
		}
		if st.IsDir() != (p.Kind == "dir") {
			return Value{}, fmt.Errorf("%w: %s: %s is not a %s", ErrUsage, p.Name, v, p.Kind)
		}
		return Value{Text: v, Files: []string{v}}, nil
	}
	return Value{}, fmt.Errorf("unknown param kind %q", p.Kind)
}

// ref returns the variable named s, or s itself as a literal.
func (r *run) ref(s string) string {
	if v, ok := r.vars[s]; ok {
		return v.Text
	}
	return s
}

func (r *run) expand(s string) string {
	return tmplRe.ReplaceAllStringFunc(s, func(m string) string {
		return r.vars[m[2:len(m)-2]].Text
	})
}

// expandWith is expand with the value name standing for text.
func (r *run) expandWith(s, name, text string) string {
	return tmplRe.ReplaceAllStringFunc(s, func(m string) string {
		if k := m[2 : len(m)-2]; k == name {
			return text
		}
		return r.vars[m[2:len(m)-2]].Text
	})
}

func (r *run) logf(format string, a ...any) {
	if r.env.Log != nil {
		fmt.Fprintf(r.env.Log, format+"\n", a...)
	}
}

func (r *run) session(capability string) (*registry.Model, runtime.Session, error) {
	m, files, err := r.env.Resolve(r.ctx, capability)
	if err != nil {
		return nil, nil, err
	}
	if s, ok := r.sessions[m.ID]; ok {
		return m, s, nil
	}
	b, err := r.env.Backend(m)
	if err != nil {
		return nil, nil, err
	}
	done := r.status(fmt.Sprintf("loading %s for %s", m.ID, capability))
	s, err := b.Open(r.ctx, m, files)
	done(err == nil)
	if err != nil {
		return nil, nil, err
	}
	r.sessions[m.ID] = s
	return m, s, nil
}

func (r *run) status(msg string) func(bool) {
	if r.env.Status == nil {
		return func(bool) {}
	}
	return r.env.Status(msg)
}

func (r *run) close() {
	for _, s := range r.sessions {
		s.Close()
	}
}
