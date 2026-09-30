package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/jgalego/mote/internal/motebook"
	"github.com/jgalego/mote/internal/task"
)

// `mote nb exec` runs the one cell it is given on standard input and prints
// its output. It is what a Jupyter kernel calls for each cell: the kernel is
// long-lived but mote is not, so what cells bind is kept in a state file the
// caller owns, and `--state FILE` reads it before the cell runs and writes it
// after. Models stay loaded between calls in mote's resident server.
//
// A cell may bind a name that is already bound, since running a cell again
// is what a notebook is for; the new value replaces the old.

func (a *app) nbExec(ctx context.Context, vals map[string]string) error {
	src, err := io.ReadAll(a.in)
	if err != nil {
		return err
	}
	name, expr := splitBinding(strings.TrimSpace(string(src)))
	if expr == "" {
		return usagef("no cell: give it on standard input")
	}
	statePath := vals["--state"]
	if name != "" && statePath == "" {
		return usagef("binding %s needs --state FILE, or the value has nowhere to be kept", name)
	}
	values, err := readState(statePath)
	if err != nil {
		return err
	}
	tasks, err := task.LoadFrom(a.tasksDir())
	if err != nil {
		return err
	}
	cell, err := prepareCell(motebook.Cell{Name: name, Expr: expr, Refs: motebook.Refs(expr)}, tasks)
	if err != nil {
		return err
	}
	for _, r := range cell.cell.Refs {
		if _, ok := values[r]; !ok {
			return usagef("{{%s}} has no value in this session", r)
		}
	}
	if cell.changes && vals["--yes"] != "true" && vals["-y"] != "true" {
		return usagef("this cell can change things, so mote asks before running it, and it cannot ask here; pass --yes to run it")
	}
	models := a.newNbModels(vals)
	defer models.close()
	if err := models.load(ctx); err != nil {
		return err
	}
	out, err := a.runToOutput(ctx, cell, "", values, vals, models)
	if err != nil {
		return err
	}
	if name != "" {
		values[name] = out.Text
		if err := writeState(statePath, values); err != nil {
			return err
		}
	}
	if out.Text != "" {
		fmt.Fprintln(a.out, out.Text)
	}
	return nil
}

// readState reads the values a session has bound. No file, or no path, is a
// session with nothing bound yet.
func readState(path string) (map[string]string, error) {
	values := map[string]string{}
	if path == "" {
		return values, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return values, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &values); err != nil {
		return nil, fmt.Errorf("%s is not a session's state: %w", path, err)
	}
	if values == nil { // the file held null
		values = map[string]string{}
	}
	return values, nil
}

func writeState(path string, values map[string]string) error {
	b, err := json.Marshal(values)
	if err != nil {
		return err
	}
	// What a session bound came from a model or a file of yours.
	return writeFileAtomic(path, b, 0o600)
}
