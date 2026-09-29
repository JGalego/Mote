package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	mrt "github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/internal/task"
)

// `mote do --plan` asks the text model for a whole pipeline at once rather
// than for a single task: one constrained generation that names the tasks,
// in order, with their arguments. mote checks the plan, shows it as the
// `mote pipe` command it amounts to, and runs it with the pipeline code.
// Writing one plan is much easier for a small model than deciding step by
// step, and the plan can be read before anything runs.

// maxPlanStages bounds a plan. Longer chains are rarely what a request
// needs, and a small model that keeps adding stages is usually lost.
const maxPlanStages = 4

const planSystem = "You turn a request into a plan: a short list of tasks run in order, " +
	"each receiving the previous task's output. Use as few stages as possible, often just one."

// planHelp explains the contract the plan must follow; the example shows
// the one idea small models miss, that {} carries the previous output.
const planHelp = `How a plan works:
- "first" is the stage that runs first. "then" lists the stages after it and is usually empty: add one only when the request asks for another step.
- "args" are the task's arguments in order, written out in full.
- In a "then" stage, {} inside an argument is replaced by the previous stage's output. If no argument contains {}, the output fills the first argument left out.
- A FILE argument can only be {} when the previous task produces files (audio, image, images), not text or code.
- Use file names exactly as the request gives them.

Example request: what is the capital of Peru
Example plan: {"first":{"task":"chat","args":["what is the capital of Peru"]},"then":[]}

Example request: summarise talk.mp3 in two bullets
Example plan: {"first":{"task":"transcribe","args":["talk.mp3"]},"then":[{"task":"chat","args":["Summarise in two bullets: {}"]}]}`

// plan is what the model writes. The first stage is separate from the rest
// so the grammar can hold it to different rules: it has no previous output
// to use.
type plan struct {
	First planStage   `json:"first"`
	Then  []planStage `json:"then"`
}

type planStage struct {
	Task string   `json:"task"`
	Args []string `json:"args"`
}

func (p plan) stages() []planStage { return append([]planStage{p.First}, p.Then...) }

// planCatalogue lists the tasks with their argument synopses, which the
// planner needs and the router does not.
func planCatalogue(tasks []task.Task) string {
	var b strings.Builder
	for _, t := range tasks {
		fmt.Fprintf(&b, "%s %s: %s (takes %s, produces %s)",
			t.ID, t.Usage(), t.Summary, strings.Join(t.In, "+"), t.Out)
		if len(t.Examples) > 0 {
			fmt.Fprintf(&b, ", e.g. %q", strings.Join(t.Examples, "; "))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// producesText reports whether a task's value is text rather than files.
func producesText(t task.Task) bool {
	switch t.Out {
	case "text", "code", "data", "diff":
		return true
	}
	return false
}

func required(p task.Param) bool { return !p.Optional && p.Default == nil }

// stageBranches returns one schema per task and argument count, so a stage
// can only name a real task and give it arguments it takes. Where a task
// takes a file or directory, the argument must be one the request named, or
// in a later stage the previous output: asked for a file, a small model
// otherwise writes a sentence there, or a plausible name that does not
// exist. A first stage must give every required argument.
func stageBranches(tasks []task.Task, files []string, first bool) []any {
	paths := append([]string{}, files...)
	if !first {
		paths = append(paths, pipeMarker)
	}
	var branches []any
	for _, t := range tasks {
	arity:
		for n := 0; n <= len(t.Params); n++ {
			if first && n < len(t.Params) && required(t.Params[n]) {
				continue
			}
			items := make([]any, n)
			for i, p := range t.Params[:n] {
				items[i] = map[string]any{"type": "string"}
				if p.Kind != "file" && p.Kind != "dir" {
					continue
				}
				// An optional file, like extract's schema, is a second
				// file; offered the only one there is, a small model
				// passes the same file twice.
				if len(paths) == 0 || (!required(p) && len(files) < 2) {
					continue arity
				}
				items[i] = map[string]any{"enum": paths}
			}
			args := map[string]any{"type": "array", "maxItems": 0}
			if n > 0 {
				args = map[string]any{"type": "array", "prefixItems": items, "minItems": n, "maxItems": n}
			}
			branches = append(branches, object(prop{"task", map[string]any{"const": t.ID}}, prop{"args", args}))
		}
	}
	return branches
}

func planSchema(tasks []task.Task, files []string) string {
	return mustJSON(object(
		prop{"first", map[string]any{"anyOf": stageBranches(tasks, files, true)}},
		prop{"then", map[string]any{"type": "array", "maxItems": maxPlanStages - 1,
			"items": map[string]any{"anyOf": stageBranches(tasks, files, false)}}},
	))
}

// namedFiles returns the words of a request that name something on disk.
func namedFiles(request string) []string {
	var files []string
	for _, w := range strings.Fields(request) {
		clean := strings.Trim(w, `"'.,;:!?`)
		if _, err := os.Stat(clean); clean != "" && err == nil {
			files = append(files, clean)
		}
	}
	return files
}

// planPrompt puts the catalogue, the rules and the request together. Files
// the request names are listed separately: a small model copies a name it
// is shown more faithfully than one it has to pick out of a sentence.
func planPrompt(request string, tasks []task.Task, files []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Tasks:\n%s\n%s\n\n", planCatalogue(tasks), planHelp)
	if len(files) > 0 {
		fmt.Fprintf(&b, "Files named in the request: %s\n", strings.Join(files, ", "))
	}
	fmt.Fprintf(&b, "Request: %s", request)
	return b.String()
}

// generateJSON runs one schema-constrained generation with the text model
// and decodes the reply into out.
func (a *app) generateJSON(ctx context.Context, profile string, sessions map[string]mrt.Session, system, prompt, schema string, out any) error {
	t := task.Task{
		ID:     "json",
		Params: []task.Param{{Name: "prompt", Kind: "text"}, {Name: "schema", Kind: "text"}},
		Steps: []task.Step{{
			Op: "generate", Cap: "text", As: "out",
			JSONSchema: "schema", System: system, Prompt: "{{prompt}}",
		}},
	}
	res, err := t.Run(ctx, a.env(profile, sessions), []string{prompt, schema}, task.Options{})
	if err != nil {
		// The task's "json step 1 (generate)" wrapper names an internal
		// detail; the cause, such as a model that is not downloaded, is
		// what the user needs.
		if inner := errors.Unwrap(err); inner != nil {
			return inner
		}
		return err
	}
	if err := json.Unmarshal([]byte(res.Text), out); err != nil {
		return fmt.Errorf("the model replied with %.200s", res.Text)
	}
	return nil
}

// usesPipe reports whether an argument takes the previous stage's value.
func usesPipe(arg string) bool { return arg == "-" || strings.Contains(arg, pipeMarker) }

// checkPlan turns a plan into pipeline stages, refusing what would fail
// part way through: unknown tasks, missing arguments, a later stage with no
// room for its input or given text where it needs a file, and files the
// model named that do not exist. Two slips small models make are repaired
// instead, and show in the command printed before anything runs. request
// stands in for {} in the first stage, withOutput says whether -o was given.
func checkPlan(p plan, tasks []task.Task, request string, withOutput bool) ([]stage, []task.Task, error) {
	all := p.stages()
	if len(all) > maxPlanStages {
		return nil, nil, fmt.Errorf("the plan has %d stages; at most %d are allowed", len(all), maxPlanStages)
	}
	stages := make([]stage, len(all))
	found := make([]task.Task, len(all))
	last := len(all) - 1
	for i, ps := range all {
		t, ok := task.Find(tasks, ps.Task)
		if !ok {
			return nil, nil, fmt.Errorf("stage %d: the plan names %q, which is not a task", i+1, ps.Task)
		}
		if len(ps.Args) > len(t.Params) {
			return nil, nil, fmt.Errorf("stage %d: %s takes at most %d arguments; usage: mote run %s %s",
				i+1, t.ID, len(t.Params), t.ID, t.Usage())
		}
		args := append([]string{}, ps.Args...)
		piped := false
		for j, arg := range args {
			kind := t.Params[j].Kind
			if !usesPipe(arg) {
				if kind == "file" || kind == "dir" {
					if _, err := os.Stat(arg); err != nil {
						return nil, nil, fmt.Errorf("stage %d: the plan gives %s the %s %q, which does not exist", i+1, t.ID, kind, arg)
					}
				}
				continue
			}
			if i == 0 {
				// Nothing comes before the first stage but the request, so
				// that is what a {} there can only mean.
				if !t.Params[j].TakesText() {
					return nil, nil, fmt.Errorf("stage 1: %s refers to a previous output, but it runs first", t.ID)
				}
				if arg == "-" {
					arg = pipeMarker
				}
				args[j] = strings.ReplaceAll(arg, pipeMarker, request)
				continue
			}
			piped = true
			if !t.Params[j].TakesText() && producesText(found[i-1]) {
				return nil, nil, fmt.Errorf("stage %d: %s needs a %s, but %s before it produces %s",
					i+1, t.ID, kind, found[i-1].ID, found[i-1].Out)
			}
		}
		if i == 0 {
			for j := len(args); j < len(t.Params); j++ {
				if required(t.Params[j]) {
					return nil, nil, fmt.Errorf("stage 1: %s is missing %s; usage: mote run %s %s",
						t.ID, strings.ToUpper(t.Params[j].Name), t.ID, t.Usage())
				}
			}
		} else if !piped {
			if len(args) < len(t.Params) {
				if next := t.Params[len(args)]; !next.TakesText() && producesText(found[i-1]) {
					return nil, nil, fmt.Errorf("stage %d: %s needs a %s, but %s before it produces %s",
						i+1, t.ID, next.Kind, found[i-1].ID, found[i-1].Out)
				}
			} else {
				// A full set of arguments with no {} usually means an
				// instruction written for the previous output, such as
				// chat "translate it to French". Small models forget the
				// {}, so it goes after a final text argument.
				j := len(args) - 1
				if j < 0 || !t.Params[j].TakesText() {
					return nil, nil, fmt.Errorf("stage %d: %s has no argument left for the previous output", i+1, t.ID)
				}
				args[j] = strings.TrimRight(args[j], " \n") + "\n\n" + pipeMarker
			}
		}
		if t.Output == "required" && (i != last || !withOutput) {
			return nil, nil, fmt.Errorf("stage %d: %s writes a file, so it must come last and needs -o", i+1, t.ID)
		}
		stages[i] = stage{text: pipeStage(t.ID, args), id: t.ID, args: args}
		found[i] = t
	}
	return stages, found, nil
}

// pipeQuote writes one argument so splitArgs reads it back unchanged.
func pipeQuote(arg string) string {
	if arg != "" && !strings.ContainsAny(arg, " \t\n\r'\"\\|") {
		return arg
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(arg) + `"`
}

func pipeStage(id string, args []string) string {
	parts := []string{id}
	for _, a := range args {
		parts = append(parts, pipeQuote(a))
	}
	return strings.Join(parts, " ")
}

// shellQuote wraps s in single quotes for a POSIX shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// planCommand is the command that runs the same thing, so a plan can be
// read, kept and rerun by hand.
func planCommand(stages []stage) string {
	if len(stages) == 1 {
		parts := []string{"mote run", stages[0].id}
		for _, a := range stages[0].args {
			parts = append(parts, shellQuote(a))
		}
		return strings.Join(parts, " ")
	}
	texts := make([]string, len(stages))
	for i, s := range stages {
		texts[i] = s.text
	}
	return "mote pipe " + shellQuote(strings.Join(texts, " | "))
}

// doPlan implements `mote do --plan`.
func (a *app) doPlan(ctx context.Context, request string, tasks []task.Task, vals map[string]string, profile string, sessions map[string]mrt.Session) error {
	var p plan
	// A word shaped like a filename that does not stat here gets the same
	// search nearby and confirm as do's own binding. This has to happen
	// before namedFiles: the schema below only ever offers a file-taking
	// task the files it finds, so a name that resolves to a subdirectory
	// must be found before the plan is generated, not after it is rejected
	// for lacking a file to use.
	if resolved, err := a.resolveMissingFile(request, candidateFilenames(request), vals); err != nil {
		return err
	} else if resolved != "" {
		request = resolved
	}
	files := namedFiles(request)
	if err := a.generateJSON(ctx, profile, sessions, planSystem, planPrompt(request, tasks, files), planSchema(tasks, files), &p); err != nil {
		return fmt.Errorf("planning: %w", err)
	}
	stages, found, err := checkPlan(p, tasks, request, firstNonEmpty(vals["-o"], vals["--output"]) != "")
	if err != nil {
		return fmt.Errorf("the plan cannot run: %w", err)
	}
	fmt.Fprintf(a.err, "%s %s\n", a.ue.Arrow(), a.ue.Dim(planCommand(stages)))
	if vals["--dry-run"] == "true" {
		return nil
	}
	if err := a.confirmChosen(found, planCommand(stages), vals); err != nil {
		return err
	}
	// As in `mote pipe`, remembered facts apply to every stage.
	remembered, err := a.memoryFor(ctx, "", nil, profile, sessions)
	if err != nil {
		return err
	}
	return a.runStages(ctx, stages, found, vals, profile, sessions, remembered, request)
}
