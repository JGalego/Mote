package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jgalego/mote/internal/motebook"
	mrt "github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/internal/task"
)

// `mote meta "GOAL"` asks the text model for a whole motebook at once: the
// cells that, run in order, get a complex goal done. Like `mote do --plan`
// it is one constrained generation, so a small model only has to write a
// plan and cannot name a task that does not exist. Unlike a plan it is kept
// as a file, with a sentence of prose above each cell, to be read, edited
// and run with `mote nb run`.
//
// The model writes tasks and never shell commands, so a generated notebook
// can only do what the tasks do.

// maxMetaCells bounds a notebook, for the same reason as maxPlanStages.
const maxMetaCells = 6

const metaUsage = `usage: mote meta "GOAL" [-o FILE] [--chat] [--run] [--force] [--yes] [--model ID] [--profile P]`

const metaSystem = "You turn a goal into a notebook: a short list of cells run in order, each a task or a short pipeline of tasks. " +
	"Use as few cells as possible."

const metaHelp = `How a notebook plan works:
- "cells" run in order. Each cell has a "note", one sentence saying what it does, and a pipeline: "first" runs first and "then" lists the stages after it. A cell is usually a single stage.
- In a "then" stage, {} inside an argument is replaced by the previous stage's output in the same cell.
- A cell reads the whole output of an earlier cell by writing {{step1}} for cell 1, {{step2}} for cell 2, and so on, inside an argument. Do this whenever a step needs what an earlier cell produced. Never refer to a cell that comes later, and never use {} for this.
- "args" are the task's arguments in order, written out in full.
- A FILE argument can only be a file the goal names, {} after a stage that produces files (audio, image, images), or {{stepN}} for an earlier cell that produces files.
- Use file names exactly as the goal gives them.

Example goal: find out what a mutex is, then explain it to a child
Example plan: {"title":"Mutexes","cells":[{"note":"Explain what a mutex is.","first":{"task":"chat","args":["What is a mutex?"]},"then":[]},{"note":"Retell it for a child.","first":{"task":"chat","args":["Explain this to a child: {{step1}}"]},"then":[]}]}

Example goal: summarise talk.mp3, then translate the summary to French
Example plan: {"title":"Talk summary","cells":[{"note":"Transcribe the talk and summarise it.","first":{"task":"transcribe","args":["talk.mp3"]},"then":[{"task":"chat","args":["Summarise in three bullets: {}"]}]},{"note":"Translate the summary.","first":{"task":"translate","args":["French","{{step1}}"]},"then":[]}]}`

// metaPlan is what the model writes: a title and the cells, each a plan of
// its own with a note for the prose above it.
type metaPlan struct {
	Title string     `json:"title"`
	Cells []metaCell `json:"cells"`
}

type metaCell struct {
	Note string `json:"note"`
	plan
}

// JSON writes the plan the way the model is asked to, which is how it is
// shown back to it to be revised.
func (p metaPlan) JSON() string {
	type stageJSON = planStage
	type cellJSON struct {
		Note  string      `json:"note"`
		First stageJSON   `json:"first"`
		Then  []stageJSON `json:"then"`
	}
	out := struct {
		Title string     `json:"title"`
		Cells []cellJSON `json:"cells"`
	}{Title: p.Title, Cells: make([]cellJSON, 0, len(p.Cells))}
	for _, c := range p.Cells {
		then := c.Then
		if then == nil {
			then = []stageJSON{}
		}
		out.Cells = append(out.Cells, cellJSON{Note: c.Note, First: c.First, Then: then})
	}
	return mustJSON(out)
}

// metaTasks are the tasks a cell can run: those a request can drive, minus
// the ones that need an output path, which a cell has no place to give.
func metaTasks(tasks []task.Task) []task.Task {
	var out []task.Task
	for _, t := range routable(tasks) {
		if t.Output != "required" {
			out = append(out, t)
		}
	}
	return out
}

// metaSchema constrains a notebook plan. Every cell is held to the same
// grammar, so where a cell may read an earlier one is checked afterwards,
// with the reason, rather than by a grammar that differs by position.
func metaSchema(tasks []task.Task, files []string) string {
	paths := append([]string{}, files...)
	for n := 1; n <= maxMetaCells; n++ {
		paths = append(paths, fmt.Sprintf("{{step%d}}", n))
	}
	cell := object(
		prop{"note", map[string]any{"type": "string", "maxLength": 200}},
		prop{"first", map[string]any{"anyOf": stageBranches(tasks, paths, true)}},
		prop{"then", map[string]any{"type": "array", "maxItems": maxPlanStages - 1,
			"items": map[string]any{"anyOf": stageBranches(tasks, paths, false)}}},
	)
	return mustJSON(object(
		prop{"title", map[string]any{"type": "string", "maxLength": 80}},
		prop{"cells", map[string]any{"type": "array", "minItems": 1, "maxItems": maxMetaCells, "items": cell}},
	))
}

func metaPrompt(goal string, tasks []task.Task, files []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Tasks:\n%s\n%s\n\n", planCatalogue(tasks), metaHelp)
	if len(files) > 0 {
		fmt.Fprintf(&b, "Files named in the goal: %s\n", strings.Join(files, ", "))
	}
	fmt.Fprintf(&b, "Goal: %s", goal)
	return b.String()
}

// metaRevisePrompt asks for a plan again with a change made to the last one.
// The plan carries every change asked for so far, so only the latest is
// needed with it.
func metaRevisePrompt(goal string, tasks []task.Task, files []string, current metaPlan, change string) string {
	return metaPrompt(goal, tasks, files) +
		"\n\nCurrent plan:\n" + current.JSON() +
		"\n\nChange requested: " + change +
		"\n\nWrite the complete revised plan, changing only what was asked."
}

// plainLine reduces text a model wrote to one line that cannot open a code
// fence or otherwise change the shape of the Markdown around it.
func plainLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "`", "'")), " ")
}

func sameArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// buildNotebook checks every cell of a plan and writes the notebook. It then
// reads the text back and insists on the cells it started with: an argument
// that a fence or a quote would garble is refused here, not found out when
// the notebook is run.
func buildNotebook(goal string, np metaPlan, tasks []task.Task) (string, error) {
	if len(np.Cells) == 0 {
		return "", fmt.Errorf("the plan has no cells")
	}
	if len(np.Cells) > maxMetaCells {
		return "", fmt.Errorf("the plan has %d cells; at most %d are allowed", len(np.Cells), maxMetaCells)
	}
	earlier := map[string]task.Task{}
	all := make([][]stage, len(np.Cells))
	read := map[string]bool{} // cells a later one reads, which must be named
	for i, c := range np.Cells {
		stages, found, err := checkStages(c.plan, tasks, planContext{notebook: true, earlier: earlier})
		if err != nil {
			return "", fmt.Errorf("cell %d: %w", i+1, err)
		}
		for _, s := range stages {
			for _, arg := range s.args {
				for _, name := range motebook.Refs(arg) {
					read[name] = true
				}
			}
		}
		all[i] = stages
		earlier[fmt.Sprintf("step%d", i+1)] = found[len(found)-1]
	}

	var b strings.Builder
	title := plainLine(np.Title)
	if title == "" {
		title = "Motebook"
	}
	fmt.Fprintf(&b, "# %s\n\n> %s\n", title, plainLine(goal))
	for i, stages := range all {
		b.WriteByte('\n')
		if note := plainLine(np.Cells[i].Note); note != "" {
			fmt.Fprintf(&b, "%s\n\n", note)
		}
		fence := "```mote"
		if name := fmt.Sprintf("step%d", i+1); read[name] {
			fence += " as=" + name
		}
		texts := make([]string, len(stages))
		for j, s := range stages {
			texts[j] = s.text
		}
		fmt.Fprintf(&b, "%s\n%s\n```\n", fence, strings.Join(texts, " | "))
	}
	text := b.String()

	book, err := motebook.Parse(text)
	if err != nil {
		return "", fmt.Errorf("the plan cannot be written as a notebook: %w", err)
	}
	cells, err := prepareCells(book, tasks)
	if err != nil {
		return "", fmt.Errorf("the plan cannot be written as a notebook: %w", err)
	}
	if len(cells) != len(all) {
		return "", fmt.Errorf("the plan cannot be written as a notebook: %d cells came back as %d", len(all), len(cells))
	}
	for i, c := range cells {
		back := c.stages
		if len(back) != len(all[i]) {
			return "", fmt.Errorf("cell %d cannot be written so that it reads back the same", i+1)
		}
		for j, s := range back {
			if s.id != all[i][j].id || !sameArgs(s.args, all[i][j].args) {
				return "", fmt.Errorf("cell %d cannot be written so that it reads back the same: an argument holds a quote or a fence", i+1)
			}
		}
	}
	return text, nil
}

func (a *app) metaCmd(ctx context.Context, args []string) error {
	vals, pos, err := flags(args, []string{"-o", "--output", "--model", "--profile"}, []string{"--chat", "--run", "--force", "--yes", "-y"})
	if err != nil {
		return err
	}
	goal := strings.TrimSpace(strings.Join(pos, " "))
	if goal == "" {
		return usagef("%s", metaUsage)
	}
	out := firstNonEmptyRaw(vals["-o"], vals["--output"])
	if out == "-" {
		out = ""
	}
	if vals["--run"] == "true" && out == "" {
		return usagef("--run needs -o FILE: the notebook has to be somewhere to run")
	}
	chat := vals["--chat"] == "true"
	if chat && !a.tty {
		return usagef("--chat asks what to change, and stdin is not a terminal")
	}
	if out != "" && vals["--force"] != "true" {
		if _, err := os.Stat(out); err == nil {
			return usagef("%s already exists; pass --force to replace it", out)
		}
	}
	tasks, err := task.LoadFrom(a.tasksDir())
	if err != nil {
		return err
	}
	usable := metaTasks(tasks)
	profile, err := a.selectModels(vals)
	if err != nil {
		return err
	}
	sessions := map[string]mrt.Session{}
	defer task.CloseSessions(sessions)

	// A file named in the goal that is not here is looked for nearby first,
	// as `mote do --plan` does: the grammar only offers a task that takes a
	// file the files it finds.
	if resolved, err := a.resolveMissingFile(goal, candidateFilenames(goal), vals); err != nil {
		return err
	} else if resolved != "" {
		goal = resolved
	}
	files := namedFiles(goal)
	schema := metaSchema(usable, files)
	replan := func(prompt string) (metaPlan, error) {
		var np metaPlan
		if err := a.generateJSON(ctx, profile, sessions, metaSystem, prompt, schema, &np); err != nil {
			return np, fmt.Errorf("planning: %w", err)
		}
		return np, nil
	}
	np, err := replan(metaPrompt(goal, usable, files))
	if err != nil {
		return err
	}
	text, err := buildNotebook(goal, np, usable)
	if err != nil {
		return fmt.Errorf("the plan cannot run: %w", err)
	}
	if chat {
		var kept bool
		if text, kept = a.metaChat(goal, np, text, usable, files, replan); !kept {
			fmt.Fprintf(a.err, "%s nothing written\n", a.ue.Dim("·"))
			return nil
		}
	}
	if out == "" {
		fmt.Fprint(a.out, text)
		return nil
	}
	if err := writeFileAtomic(out, []byte(text), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(a.err, "%s wrote %s\n", a.ue.OK(), out)
	if vals["--run"] != "true" {
		fmt.Fprintf(a.err, "%s\n", a.ue.Dim("run it with: mote nb run "+shellQuote(out)))
		return nil
	}
	// The file is the notebook; nothing about writing it applies to running it.
	runVals := map[string]string{}
	for k, v := range vals {
		if k != "-o" && k != "--output" && k != "--force" {
			runVals[k] = v
		}
	}
	return a.nbRun(ctx, out, runVals)
}

// metaChat shows the notebook a plan makes and asks what to change, until
// you accept it, which Enter or the end of input does, or give it up with q.
// A change goes to the model with the current plan, and comes back checked
// like the first: one that cannot run is reported and the last good plan
// stays, so nothing is lost by asking. The notebook is shown on stderr, which
// leaves stdout for the result.
func (a *app) metaChat(goal string, np metaPlan, text string, tasks []task.Task, files []string,
	replan func(prompt string) (metaPlan, error)) (string, bool) {
	sc := bufio.NewScanner(a.in)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for {
		fmt.Fprintf(a.err, "\n%s\n", text)
		prompt := a.ue.Accent("change something? describe it, Enter to keep it, q to quit › ")
		line, _ := a.readTurnPrompt(sc, true, prompt)
		switch strings.ToLower(line) {
		case "":
			return text, true // Enter, or the end of the input
		case "q", "quit":
			return "", false
		}
		revised, err := replan(metaRevisePrompt(goal, tasks, files, np, line))
		if err == nil {
			var next string
			if next, err = buildNotebook(goal, revised, tasks); err == nil {
				if next == text {
					fmt.Fprintf(a.err, "%s no change\n", a.ue.Dim("·"))
					continue
				}
				np, text = revised, next
				continue
			}
			err = fmt.Errorf("the revised plan cannot run: %w", err)
		}
		fmt.Fprintf(a.err, "%s %v; keeping the last plan\n", a.ue.Fail(), err)
	}
}
