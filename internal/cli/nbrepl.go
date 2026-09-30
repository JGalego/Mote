package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"

	"github.com/jgalego/mote/internal/motebook"
	"github.com/jgalego/mote/internal/task"
)

// `mote nb console FILE` is a motebook you type into: each line is a cell, run
// as soon as it is entered, its output printed below it and both kept in the
// file, the way a Jupyter cell is. Cells share what they bind by name, as in
// `mote nb run`, and models stay loaded from one cell to the next.
//
// With no FILE the session lives in memory. When it ends at a terminal mote
// asks whether to keep what was run, and where; /save FILE does it at any
// point, after which the session is a notebook on that file like any other.

const nbConsoleHelp = `Type a cell and press Enter to run it; a line ending in \ continues onto the next.
  chat "what is the capital of France"           a task
  code "reverse a string" | chat "explain: {}"   or a pipeline
  city = chat "capital of France"                keep the output as {{city}} for later cells
  /cells  list the cells   /undo  drop the last one   /save FILE  keep them in a file   /help  this   /exit  or Ctrl-D
With no file the cells live in memory; on exit you are asked whether to save them.`

// bindingRe reads "name = pipeline". A task id or a shell stage never has
// an = after its first word, so this cannot take a cell for a binding.
var bindingRe = regexp.MustCompile(`(?s)^([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(\S.*)$`)

func splitBinding(line string) (name, expr string) {
	if m := bindingRe.FindStringSubmatch(line); m != nil {
		return m[1], m[2]
	}
	return "", line
}

// valuesOf reads back what a notebook's cells have bound.
func valuesOf(book *motebook.Book) map[string]string {
	values := map[string]string{}
	for _, c := range book.Cells {
		if c.Name != "" && c.Output != nil {
			values[c.Name] = c.Output.Text
		}
	}
	return values
}

// cellCount says how many cells, in words that agree.
func cellCount(n int) string {
	if n == 1 {
		return "1 cell"
	}
	return fmt.Sprintf("%d cells", n)
}

// defaultSessionName is what saving a session offers as its file name.
const defaultSessionName = "session.mote.md"

// nbSession is a notebook being typed into. Its path is empty while it lives
// only in memory.
type nbSession struct {
	a      *app
	path   string
	book   *motebook.Book
	tasks  []task.Task
	values map[string]string
	models *nbModels
	vals   map[string]string
	yes    bool
	sc     *bufio.Scanner
	undo   []string // the notebook as it was before each cell added here
}

func (a *app) nbConsole(ctx context.Context, path string, vals map[string]string) error {
	var src []byte
	if path != "" {
		var err error
		if src, err = os.ReadFile(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	// A session with no file is saved to -o when it ends, without asking.
	saveTo := firstNonEmptyRaw(vals["-o"], vals["--output"])
	if saveTo != "" {
		if _, err := os.Stat(saveTo); err == nil {
			return usagef("%s already exists; open it with `mote nb console %s`, or pick another name", saveTo, saveTo)
		}
	}
	book, err := motebook.Parse(string(src))
	if err != nil {
		return usagef("%s: %v", path, err)
	}
	tasks, err := task.LoadFrom(a.tasksDir())
	if err != nil {
		return err
	}
	s := &nbSession{
		a: a, path: path, book: book, tasks: tasks, values: valuesOf(book),
		models: a.newNbModels(vals), vals: vals,
		yes: vals["--yes"] == "true" || vals["-y"] == "true",
		sc:  bufio.NewScanner(a.in),
	}
	defer s.models.close()
	s.sc.Buffer(make([]byte, 1<<20), 1<<20)

	live := a.tty && a.uo.Live()
	if live {
		where := path
		if where == "" {
			where = "not saved"
		}
		fmt.Fprintln(a.err, a.ue.Dim(fmt.Sprintf("%s · %s · /help", where, cellCount(len(book.Cells)))))
	}
	for {
		prompt := a.ue.Accent(fmt.Sprintf("In [%d]: ", len(s.book.Cells)+1))
		line, ok := a.readTurnPrompt(s.sc, live, prompt)
		if !ok {
			// Ctrl-D ends the scanner for good, so what is asked next is
			// read afresh.
			if err := s.sc.Err(); err != nil {
				return err
			}
			s.sc = bufio.NewScanner(a.in)
			return s.finish(saveTo)
		}
		var err error
		switch {
		case line == "":
			continue
		case line == "/exit" || line == "/quit" || line == "/bye":
			return s.finish(saveTo)
		case line == "/save" || strings.HasPrefix(line, "/save "):
			err = s.save(strings.TrimSpace(strings.TrimPrefix(line, "/save")))
		case line == "/help" || line == "/?":
			fmt.Fprintln(a.err, nbConsoleHelp)
			continue
		case line == "/cells":
			s.list()
			continue
		case line == "/undo":
			err = s.undoLast()
		case strings.HasPrefix(line, "/"):
			err = fmt.Errorf("unknown command %s; /help lists them", strings.Fields(line)[0])
		default:
			err = s.run(ctx, line)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintf(a.err, "%s %v\n", a.ue.Fail(), err)
		}
	}
}

func (s *nbSession) list() {
	for i, c := range s.book.Cells {
		name := ""
		if c.Name != "" {
			name = c.Name + " = "
		}
		fmt.Fprintf(s.a.out, "%d  %s%s\n", i+1, name, oneLine(c.Expr))
	}
}

// restore puts the notebook back as it was, and the values with it.
func (s *nbSession) restore(text string) error {
	book, err := motebook.Parse(text)
	if err != nil {
		return err
	}
	s.book, s.values = book, valuesOf(book)
	return nil
}

// flush writes the notebook to its file, if it has one yet.
func (s *nbSession) flush() error {
	if s.path == "" {
		return nil
	}
	return writeFileAtomic(s.path, []byte(s.book.String()), 0o644)
}

// save keeps a session that lives in memory in a file, which it is from then
// on backed by. It does not replace a file that is there.
func (s *nbSession) save(path string) error {
	if s.path != "" {
		return fmt.Errorf("this session is already saved in %s", s.path)
	}
	if path == "" {
		return errors.New("usage: /save FILE")
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists; pick another name", path)
	}
	if err := writeFileAtomic(path, []byte(s.book.String()), 0o644); err != nil {
		return err
	}
	s.path = path
	fmt.Fprintf(s.a.err, "%s saved %s in %s\n", s.a.ue.OK(), cellCount(len(s.book.Cells)), path)
	return nil
}

// finish ends a session. One that lives in memory and ran something is kept
// if saveTo says where, or if you say where when asked; without a terminal
// to ask at, it says what is being dropped rather than dropping it quietly.
func (s *nbSession) finish(saveTo string) error {
	n := len(s.book.Cells)
	if s.path != "" || n == 0 {
		return nil
	}
	if saveTo != "" {
		return s.save(saveTo)
	}
	if !s.a.tty {
		fmt.Fprintf(s.a.err, "%s %s not saved; pass -o FILE to keep them\n", s.a.ue.Warn(), cellCount(n))
		return nil
	}
	if !s.ask(fmt.Sprintf("save the %s you ran to a file? [y/N] ", cellCount(n))) {
		return nil
	}
	for {
		name := strings.TrimSpace(s.answer(fmt.Sprintf("file name [%s], or n to discard: ", defaultSessionName)))
		switch strings.ToLower(name) {
		case "n", "no":
			return nil
		case "":
			name = defaultSessionName
		}
		err := s.save(name)
		if err == nil {
			return nil
		}
		// Say why and ask again: the work is only in memory.
		fmt.Fprintf(s.a.err, "%s %v\n", s.a.ue.Fail(), err)
	}
}

// answer prints a prompt and reads the reply, empty if there is none.
func (s *nbSession) answer(prompt string) string {
	fmt.Fprint(s.a.err, prompt)
	if !s.sc.Scan() {
		fmt.Fprintln(s.a.err)
		return "n" // no more input: an answer that keeps nothing and asks nothing more
	}
	return s.sc.Text()
}

// ask is answer for a question with a yes or a no.
func (s *nbSession) ask(prompt string) bool {
	l := strings.ToLower(strings.TrimSpace(s.answer(prompt)))
	return l == "y" || l == "yes"
}

func (s *nbSession) undoLast() error {
	if len(s.undo) == 0 {
		return errors.New("nothing to undo; only cells added in this session can be dropped")
	}
	n := len(s.book.Cells)
	if err := s.restore(s.undo[len(s.undo)-1]); err != nil {
		return err
	}
	s.undo = s.undo[:len(s.undo)-1]
	if err := s.flush(); err != nil {
		return err
	}
	fmt.Fprintf(s.a.err, "%s dropped cell %d\n", s.a.ue.OK(), n)
	return nil
}

// run adds a cell, runs it and prints what it produced. A cell that cannot
// be added or that fails is not kept: the notebook is left as it was, so it
// holds only cells with an output to show.
func (s *nbSession) run(ctx context.Context, line string) error {
	name, expr := splitBinding(line)
	before := s.book.String()
	idx, err := s.book.Append(name, expr)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		if rerr := s.restore(before); rerr != nil {
			return rerr
		}
		return err
	}
	cell, err := prepareCell(s.book.Cells[idx], s.tasks)
	if err != nil {
		return fail(locateCell(idx, s.book.Cells[idx], err))
	}
	inputs := map[string]string{}
	for _, r := range cell.cell.Refs {
		v, ok := s.values[r]
		if !ok {
			return fail(fmt.Errorf("{{%s}} has no output yet; run the notebook with `mote nb run` first", r))
		}
		inputs[r] = v
	}
	if cell.changes {
		if err := s.confirm(cell); err != nil {
			return fail(err)
		}
	}
	if err := s.models.load(ctx); err != nil {
		return fail(err)
	}
	done := s.a.status(fmt.Sprintf("cell %d", idx+1))
	out, err := s.a.runToOutput(ctx, cell, motebook.Key(cell.cell, inputs), s.values, s.vals, s.models)
	done(err == nil)
	if err != nil {
		return fail(err)
	}
	s.book.Set(idx, out)
	if err := s.flush(); err != nil {
		return fail(err)
	}
	s.undo = append(s.undo, before)
	if name != "" {
		s.values[name] = out.Text
	}
	if out.Text != "" {
		fmt.Fprintln(s.a.out, out.Text)
	}
	return nil
}

// confirm asks before a cell that can change things runs, the answer coming
// from the same input the cells do.
func (s *nbSession) confirm(c nbCell) error {
	if s.yes {
		return nil
	}
	if !s.a.tty {
		return usagef("this cell can change things, so mote asks before running it, and stdin is not a terminal; pass --yes to run it")
	}
	fmt.Fprintf(s.a.err, "%s run %s? [y/N] ", s.a.ue.Warn(), s.a.ue.Bold(oneLine(c.cell.Expr)))
	if !s.sc.Scan() {
		return errors.New("not run")
	}
	if l := strings.ToLower(strings.TrimSpace(s.sc.Text())); l != "y" && l != "yes" {
		return errors.New("not run")
	}
	return nil
}
