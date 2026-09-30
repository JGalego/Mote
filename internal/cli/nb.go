package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jgalego/mote/internal/atomicfile"
	"github.com/jgalego/mote/internal/motebook"
	mrt "github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/internal/task"
)

// A motebook is a Markdown file whose ```mote blocks are cells, each one a
// task or a pipeline written the way `mote pipe` takes it. `mote nb run`
// runs them in order and writes each cell's output into the file, right
// under the cell, so the file is the record. A cell whose text and inputs
// are unchanged since its output was written is not run again, and one that
// binds a name with as=NAME hands its output to later cells as {{NAME}}.

const nbUsage = "usage: mote nb run FILE [-o FILE|-] [--force] [--dry-run] [--yes] [--model ID] [--profile P]\n" +
	"       mote nb console [FILE | -o FILE] [--yes] [--model ID] [--profile P]\n" +
	"       mote nb exec [--state FILE] [--yes] [--model ID] [--profile P]\n" +
	"       mote nb serve FILE [--port N] [--root DIR] [--open] [--yes] [--model ID] [--profile P]\n" +
	"       mote nb export FILE [-o FILE|-] [--force]\n" +
	"       mote nb import FILE.ipynb [-o FILE|-] [--force] [--any-kernel]"

// nbCell is a cell ready to run: parsed, with its tasks found.
type nbCell struct {
	cell   motebook.Cell
	stages []stage
	found  []task.Task
	// changes is set when running the cell can change things: a shell
	// stage, or a task marked "asks". Such a cell is confirmed before the
	// notebook runs and never skipped, since running it again is the point.
	changes bool
}

func (a *app) nbCmd(ctx context.Context, args []string) error {
	vals, pos, err := flags(args, []string{"-o", "--output", "--model", "--profile", "--state", "--port", "--root"}, []string{"--yes", "-y", "--force", "--dry-run", "--any-kernel", "--open"})
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usagef("%s", nbUsage)
	}
	// Each subcommand takes the flags that mean something to it.
	allowed := map[string][]string{
		"run":     {"-o", "--output", "--force", "--dry-run"},
		"console": {"-o", "--output"},
		"exec":    {"--state"},
		"serve":   {"--port", "--root", "--open"},
		"export":  {"-o", "--output", "--force"},
		"import":  {"-o", "--output", "--force", "--any-kernel"},
	}
	own, ok := allowed[pos[0]]
	if !ok {
		return usagef("%s", nbUsage)
	}
	for _, f := range []string{"-o", "--output", "--force", "--dry-run", "--state", "--any-kernel", "--port", "--root", "--open"} {
		if vals[f] == "" || contains(own, f) {
			continue
		}
		return usagef("%s does not apply to `mote nb %s`", f, pos[0])
	}
	switch {
	case pos[0] == "exec" && len(pos) == 1:
		return a.nbExec(ctx, vals)
	case pos[0] == "run" && len(pos) == 2:
		return a.nbRun(ctx, pos[1], vals)
	case pos[0] == "serve" && len(pos) == 2:
		return a.nbServe(ctx, pos[1], vals)
	case pos[0] == "export" && len(pos) == 2:
		return a.nbExport(pos[1], vals)
	case pos[0] == "import" && len(pos) == 2:
		return a.nbImport(pos[1], vals)
	case pos[0] == "console" && len(pos) <= 2:
		path := ""
		if len(pos) == 2 {
			if firstNonEmptyRaw(vals["-o"], vals["--output"]) != "" {
				return usagef("-o names where a session with no FILE is saved; a session on FILE saves to it")
			}
			path = pos[1]
		}
		return a.nbConsole(ctx, path, vals)
	}
	return usagef("%s", nbUsage)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// prepareCells parses every cell and finds its tasks before anything runs,
// so a typo in the last cell does not surface after minutes of generation.
func prepareCells(book *motebook.Book, tasks []task.Task) ([]nbCell, error) {
	cells := make([]nbCell, len(book.Cells))
	for i, c := range book.Cells {
		var err error
		if cells[i], err = prepareCell(c, tasks); err != nil {
			return nil, locateCell(i, c, err)
		}
	}
	return cells, nil
}

// locateCell says where in a notebook a cell's problem is.
func locateCell(i int, c motebook.Cell, err error) error {
	return usagef("cell %d (line %d): %v", i+1, c.Line, err)
}

// prepareCell parses one cell and finds its tasks.
func prepareCell(c motebook.Cell, tasks []task.Task) (nbCell, error) {
	stages, err := parseStages(c.Expr)
	if err != nil {
		return nbCell{}, err
	}
	found, err := resolveStages(stages, tasks)
	if err != nil {
		return nbCell{}, err
	}
	nc := nbCell{cell: c, stages: stages, found: found}
	for j, s := range stages {
		if s.shell != "" {
			// A shell command is text handed to a shell: a value that came
			// from a model must not be able to become part of it.
			if len(motebook.Refs(s.shell)) > 0 {
				return nbCell{}, usagef("{{name}} cannot go into a shell command; pipe the value in and use {} instead")
			}
			nc.changes = true
		} else if found[j].Asks {
			nc.changes = true
		}
	}
	return nc, nil
}

// nbModels holds what running cells needs from the models, loaded when the
// first cell that has to run asks for it and kept for the ones after.
type nbModels struct {
	a          *app
	vals       map[string]string
	profile    string
	remembered string
	sessions   map[string]mrt.Session
	ready      bool
}

func (a *app) newNbModels(vals map[string]string) *nbModels {
	return &nbModels{a: a, vals: vals, sessions: map[string]mrt.Session{}}
}

func (m *nbModels) load(ctx context.Context) error {
	if m.ready {
		return nil
	}
	var err error
	if m.profile, err = m.a.selectModels(m.vals); err != nil {
		return err
	}
	if m.remembered, err = m.a.memoryFor(ctx, "", nil, m.profile, m.sessions); err != nil {
		return err
	}
	m.ready = true
	return nil
}

func (m *nbModels) close() { task.CloseSessions(m.sessions) }

// confirmNotebook asks once, before anything runs, when cells can change
// things. A notebook may have been written by a model (`mote meta`), so
// running it is not the same as typing its cells.
func (a *app) confirmNotebook(cells []nbCell, vals map[string]string) error {
	var risky []string
	for i, c := range cells {
		if c.changes {
			risky = append(risky, fmt.Sprintf("cell %d (line %d): %s", i+1, c.cell.Line, oneLine(c.cell.Expr)))
		}
	}
	if len(risky) == 0 || vals["--yes"] == "true" || vals["-y"] == "true" {
		return nil
	}
	if !a.tty {
		return usagef("%d cell(s) can change things, so mote asks before running them, and stdin is not a terminal; pass --yes to run them", len(risky))
	}
	fmt.Fprintf(a.err, "%s these cells can change things:\n", a.ue.Warn())
	for _, r := range risky {
		fmt.Fprintf(a.err, "  %s\n", r)
	}
	fmt.Fprintf(a.err, "run the notebook? [y/N] ")
	line, _ := bufio.NewReader(a.in).ReadString('\n')
	if l := strings.ToLower(strings.TrimSpace(line)); l != "y" && l != "yes" {
		return errors.New("not run")
	}
	return nil
}

func (a *app) nbRun(ctx context.Context, path string, vals map[string]string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	book, err := motebook.Parse(string(src))
	if err != nil {
		return usagef("%s: %v", path, err)
	}
	if len(book.Cells) == 0 {
		return usagef("%s has no cells; a cell is a fenced block starting with ```mote", path)
	}
	tasks, err := task.LoadFrom(a.tasksDir())
	if err != nil {
		return err
	}
	cells, err := prepareCells(book, tasks)
	if err != nil {
		return err
	}
	dry := vals["--dry-run"] == "true"
	if !dry {
		if err := a.confirmNotebook(cells, vals); err != nil {
			return err
		}
	}

	dest := firstNonEmptyRaw(vals["-o"], vals["--output"], path)
	save := func() error {
		if dest == "-" {
			return nil
		}
		return writeFileAtomic(dest, []byte(book.String()), 0o644)
	}

	// Models are only loaded when a cell has to run.
	models := a.newNbModels(vals)
	defer models.close()

	values := map[string]string{}
	bindValue := func(c nbCell, v string) {
		if c.cell.Name != "" {
			values[c.cell.Name] = v
		}
	}
	stale := map[string]bool{} // names whose cell has to run again, for --dry-run
	ran, kept := 0, 0
	for i, c := range cells {
		label := fmt.Sprintf("cell %d/%d", i+1, len(cells))
		if n := c.cell.Name; n != "" {
			label += " " + n
		}
		inputs := map[string]string{}
		upstream := false
		for _, r := range c.cell.Refs {
			inputs[r] = values[r]
			upstream = upstream || stale[r]
		}
		key := motebook.Key(c.cell, inputs)
		out := c.cell.Output
		if !upstream && vals["--force"] != "true" && out != nil && out.Key != "" && out.Key == key {
			bindValue(c, out.Text)
			kept++
			fmt.Fprintf(a.err, "%s %s %s\n", a.ue.OK(), label, a.ue.Dim("unchanged"))
			continue
		}
		if dry {
			if out != nil {
				bindValue(c, out.Text)
			}
			if c.cell.Name != "" {
				stale[c.cell.Name] = true
			}
			fmt.Fprintf(a.err, "%s %s %s\n", a.ue.Dim("·"), label, a.ue.Dim("would run: "+oneLine(c.cell.Expr)))
			continue
		}
		if err := models.load(ctx); err != nil {
			return err
		}
		started := time.Now()
		res, err := a.runToOutput(ctx, c, key, values, vals, models)
		a.cellDone(label, started, err == nil)
		if err != nil {
			return fmt.Errorf("cell %d (line %d): %w", i+1, c.cell.Line, err)
		}
		bindValue(c, res.Text)
		book.Set(i, res)
		if err := save(); err != nil {
			return err
		}
		ran++
	}
	if dest == "-" && !dry {
		fmt.Fprint(a.out, book.String())
	}
	fmt.Fprintf(a.err, "%s %d run, %d unchanged\n", a.ue.OK(), ran, kept)
	return nil
}

// cellDone reports how long a cell took, once it is done. A cell is not a
// spinner while it runs: the tasks in it draw their own, loading a model and
// thinking, and two spinners on one line overwrite each other.
func (a *app) cellDone(label string, started time.Time, ok bool) {
	mark := a.ue.OK()
	if !ok {
		mark = a.ue.Fail()
	}
	fmt.Fprintf(a.err, "%s %s %s\n", mark, label, a.ue.Dim(fmt.Sprintf("%.1fs", time.Since(started).Seconds())))
}

// runToOutput runs a cell and returns the output to keep under it. key is
// the fingerprint it would have if it could be trusted to come out the same
// again; a cell that can change things, or that produced files, which may
// be gone by the next run, is kept without one and so always runs.
func (a *app) runToOutput(ctx context.Context, c nbCell, key string, values, vals map[string]string, m *nbModels) (motebook.Output, error) {
	text, files, err := a.runCell(ctx, c, values, vals, m)
	if err != nil {
		return motebook.Output{}, err
	}
	if files || c.changes {
		key = ""
	}
	return motebook.Output{Key: key, Text: text}, nil
}

// runCell runs one cell with the values bound so far and returns what it
// produced: its text, or the paths of the files it made, one per line, in
// which case files is set.
func (a *app) runCell(ctx context.Context, c nbCell, values, vals map[string]string, m *nbModels) (text string, files bool, err error) {
	lookup := func(name string) (string, bool) {
		v, ok := values[name]
		return v, ok
	}
	stages := append([]stage(nil), c.stages...)
	for i := range stages {
		stages[i].expand = func(s string) string { return motebook.Substitute(s, lookup) }
	}
	r, err := a.chain(ctx, stages, c.found, vals, m.profile, m.sessions, m.remembered, "", false)
	if err != nil {
		return "", false, err
	}
	if len(r.res.Files) > 0 {
		return strings.Join(r.res.Files, "\n"), true, nil
	}
	return strings.TrimRight(r.res.Text, "\n"), false, nil
}

// firstNonEmptyRaw returns the first argument that is not empty, unlike
// firstNonEmpty, which keeps only a value's first word: fine for a model id,
// wrong for a path.
func firstNonEmptyRaw(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// writeFileAtomic replaces path with data in one step, so a notebook that is
// saved after every cell is never left half-written. A file that exists
// keeps its mode; a new one gets mode.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	return atomicfile.Write(path, data, mode)
}
