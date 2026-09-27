package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	mrt "github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/internal/task"
	"github.com/jgalego/mote/internal/ui"
)

// A pipeline chains tasks the way a shell chains commands, but in one
// process, so models stay loaded between stages:
//
//	mote pipe 'transcribe meeting.m4a | chat "summarise in 3 bullets: {}"'
//
// Each stage receives the previous stage's value. {} in an argument is
// replaced by it (the text, or a file's path), a bare - means the same, and
// with neither the value fills the stage's first unset parameter.
//
// A stage beginning with ! or sh: is a shell command: it reads the value on
// stdin and its output becomes the next value, so mote and ordinary tools
// can be mixed. Both spellings mean the same thing; sh: exists because an
// interactive bash or zsh expands ! inside double quotes as history, so
// "… | !python3 -" can turn into something else entirely before mote sees
// it. Single quotes around the pipeline also prevent that.
type stage struct {
	text  string   // the stage as written, for error messages
	shell string   // command line after '!', empty for a task stage
	id    string   // task id
	args  []string // arguments as written, before substitution
}

const pipeMarker = "{}"

// shellMarker reports which prefix marks a stage as a shell command.
func shellMarker(text string) string {
	for _, m := range []string{"!", "sh:"} {
		if strings.HasPrefix(text, m) {
			return m
		}
	}
	return ""
}

// splitOutsideQuotes splits s on sep, ignoring separators inside single or
// double quotes so a prompt may contain them.
func splitOutsideQuotes(s string, sep byte) []string {
	var parts []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
		case quote == 0 && c == sep:
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	return append(parts, cur.String())
}

// splitArgs tokenises one stage the way a shell would: whitespace separates
// arguments, quotes group them, and a backslash escapes the next character.
func splitArgs(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	var quote byte
	held := false // cur holds an argument, even an empty quoted one
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			held = true
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && (c == '\'' || c == '"'):
			quote, held = c, true
		case quote == 0 && (c == ' ' || c == '\t' || c == '\n'):
			if held {
				args = append(args, cur.String())
				cur.Reset()
				held = false
			}
		default:
			cur.WriteByte(c)
			held = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed %c quote", quote)
	}
	if held {
		args = append(args, cur.String())
	}
	return args, nil
}

// parsePipeline splits an expression into stages.
func parsePipeline(expr string) ([]stage, error) {
	var stages []stage
	for _, part := range splitOutsideQuotes(expr, '|') {
		text := strings.TrimSpace(part)
		if text == "" {
			return nil, usagef("empty stage in pipeline; stages are separated by a single |")
		}
		if marker := shellMarker(text); marker != "" {
			cmd := strings.TrimSpace(strings.TrimPrefix(text, marker))
			if cmd == "" {
				return nil, usagef("empty shell command after %s", marker)
			}
			stages = append(stages, stage{text: text, shell: cmd})
			continue
		}
		args, err := splitArgs(text)
		if err != nil {
			return nil, usagef("%s: %v", text, err)
		}
		stages = append(stages, stage{text: text, id: args[0], args: args[1:]})
	}
	if len(stages) < 2 {
		return nil, usagef("a pipeline needs at least two stages; use `mote run` for one")
	}
	return stages, nil
}

// bind places the value coming down the pipe into a stage's arguments. It
// returns one argument list per run: more than one when several files arrive
// at a stage that takes a single file, which is then run for each of them.
func bind(t task.Task, args []string, in task.Value, first bool) ([][]string, error) {
	if first {
		return [][]string{args}, nil
	}
	values := []string{in.Text}
	if len(in.Files) > 0 {
		values = in.Files
	}
	placed := false
	for _, a := range args {
		if a == "-" || strings.Contains(a, pipeMarker) {
			placed = true
		}
	}
	if placed {
		out := make([][]string, 0, len(values))
		for _, v := range values {
			next := make([]string, len(args))
			for i, a := range args {
				if a == "-" {
					next[i] = v
					continue
				}
				next[i] = strings.ReplaceAll(a, pipeMarker, v)
			}
			out = append(out, next)
		}
		return out, nil
	}
	// No marker: fill the first parameter the stage did not give a value.
	if len(args) >= len(t.Params) {
		return nil, usagef("%s already has every argument; write %s or - where the piped value goes",
			t.ID, pipeMarker)
	}
	p := t.Params[len(args)]
	if p.Kind != "text" && len(in.Files) == 0 {
		return nil, usagef("%s needs a %s for %s, but the previous stage produced text; write it to a file first",
			t.ID, p.Kind, p.Name)
	}
	out := make([][]string, 0, len(values))
	for _, v := range values {
		out = append(out, append(append([]string{}, args...), v))
	}
	return out, nil
}

// runShell runs a ! stage, feeding it the piped value on stdin: the text, or
// one file path per line. Its standard output becomes the next value.
func (a *app) runShell(ctx context.Context, s stage, in task.Value) (task.Value, error) {
	stdin := in.Text
	if len(in.Files) > 0 {
		stdin = strings.Join(in.Files, "\n") + "\n"
	}
	line := strings.ReplaceAll(s.shell, pipeMarker, strings.Join(in.Files, " "))
	shell, flag := "/bin/sh", "-c"
	if runtime.GOOS == "windows" {
		shell, flag = "cmd", "/c"
	}
	cmd := exec.CommandContext(ctx, shell, flag, line)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stderr = a.err
	done := a.status("running " + s.shell)
	out, err := cmd.Output()
	done(err == nil)
	if err != nil {
		return task.Value{}, fmt.Errorf("%s: %w", s.text, err)
	}
	return task.Value{Text: string(out)}, nil
}

// pipe runs several tasks in one process, passing each stage's value to the
// next and keeping models loaded across stages.
func (a *app) pipe(ctx context.Context, args []string) error {
	vals, pos, err := flags(args, []string{"-o", "--output", "--model", "--profile"}, []string{"--apply", "--trace"})
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usagef(`usage: mote pipe "TASK ARGS | TASK ARGS | !COMMAND"`)
	}
	stages, err := parsePipeline(strings.Join(pos, " "))
	if err != nil {
		return err
	}
	tasks, err := task.LoadFrom(a.tasksDir())
	if err != nil {
		return err
	}
	found, err := resolveStages(stages, tasks)
	if err != nil {
		return err
	}
	profile, err := a.selectModels(vals)
	if err != nil {
		return err
	}
	sessions := map[string]mrt.Session{}
	defer task.CloseSessions(sessions)
	// Facts apply to every stage; --continue and --recall belong to a
	// single request, which a pipeline is not.
	remembered, err := a.memoryFor(ctx, "", nil, profile, sessions)
	if err != nil {
		return err
	}
	return a.runStages(ctx, stages, found, vals, profile, sessions, remembered, strings.Join(pos, " "))
}

// resolveStages finds the task for every stage up front: a typo in the last
// stage should not surface after minutes of generation. Shell stages have
// no task and get a zero one.
func resolveStages(stages []stage, tasks []task.Task) ([]task.Task, error) {
	found := make([]task.Task, len(stages))
	for i, s := range stages {
		if s.shell != "" {
			continue
		}
		t, ok := task.Find(tasks, s.id)
		if !ok {
			return nil, usagef("unknown task %q in stage %d; see `mote tasks`", s.id, i+1)
		}
		if len(s.args) > len(t.Params) {
			return nil, usagef("too many arguments for %s; usage: mote run %s %s", t.ID, t.ID, t.Usage())
		}
		found[i] = t
	}
	return found, nil
}

// runStages runs resolved stages in order, each receiving the value of the
// one before, and emits the last one's result. request is what gets
// recorded in the history.
func (a *app) runStages(ctx context.Context, stages []stage, found []task.Task, vals map[string]string,
	profile string, sessions map[string]mrt.Session, remembered, request string) error {
	out := firstNonEmpty(vals["-o"], vals["--output"])
	defer os.RemoveAll(a.env(profile, nil).TempDir)

	last := len(stages) - 1
	var val task.Value
	var res task.Result
	// trace shows what a stage handed on, which is otherwise invisible:
	// only the last stage's output reaches stdout. It goes to stderr so a
	// redirected pipeline still captures the result alone.
	trace := func(i int, name string, v task.Value) {
		if vals["--trace"] != "true" || i == last {
			return
		}
		body := strings.TrimRight(v.Text, "\n")
		if len(v.Files) > 0 {
			body = strings.Join(v.Files, "\n")
		}
		fmt.Fprintf(a.err, "%s\n%s\n", a.ue.Dim(fmt.Sprintf("── stage %d (%s) ──", i+1, name)), body)
	}

	// empty reports a stage that produced nothing to pass on. Sending an
	// empty value to the next task would have it answer a question nobody
	// asked, so the pipeline stops and says which stage ran dry.
	empty := func(i int, name string, v task.Value) error {
		if i == last || strings.TrimSpace(v.Text) != "" || len(v.Files) > 0 {
			return nil
		}
		return fmt.Errorf("stage %d (%s) produced nothing for the next stage", i+1, name)
	}

	for i, s := range stages {
		if s.shell != "" {
			var err error
			if val, err = a.runShell(ctx, s, val); err != nil {
				return err
			}
			if err := empty(i, s.shell, val); err != nil {
				return err
			}
			trace(i, s.shell, val)
			res = task.Result{Value: val}
			continue
		}
		t := found[i]
		runs, err := bind(t, s.args, val, i == 0)
		if err != nil {
			return err
		}
		env := a.env(profile, sessions)
		env.Memory = remembered
		var code *ui.CodeStream
		if i == last {
			if out == "" && (t.Out == "code" || t.Out == "data") {
				code = a.uo.CodeStream(highlightLang(t, langHint(runs[0])))
			}
			// Only the last stage streams: earlier ones are inputs to the
			// next task, not output for the reader.
			if out == "" && a.uo.Live() && len(runs) == 1 {
				if code != nil {
					env.Stream, env.Lang = code.Write, code.Lang
				} else {
					env.Stream = func(tok string) { fmt.Fprint(a.out, tok) }
				}
			}
		}
		opt := task.Options{Apply: vals["--apply"] == "true"}
		if i == last {
			opt.Output = out
		}
		var texts, files []string
		for _, stageArgs := range runs {
			r, err := t.Run(ctx, env, stageArgs, opt)
			if err != nil {
				return fmt.Errorf("stage %d (%s): %w", i+1, t.ID, err)
			}
			res = r
			if strings.TrimSpace(r.Text) != "" {
				texts = append(texts, strings.TrimRight(r.Text, "\n"))
			}
			files = append(files, r.Files...)
		}
		val = task.Value{Text: strings.Join(texts, "\n"), Files: files}
		res.Value = val
		if err := empty(i, t.ID, val); err != nil {
			return err
		}
		trace(i, t.ID, val)
		if i == last {
			if res.Lang == "" && len(runs) > 0 {
				res.Lang = langHint(runs[0])
			}
			a.record(t, request, res)
			return a.emit(t, res, out, code)
		}
	}
	// The pipeline ended with a shell stage, which produced plain text.
	return a.emit(task.Task{}, res, out, nil)
}
