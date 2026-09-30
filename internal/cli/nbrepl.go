package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jgalego/mote/internal/motebook"
	"github.com/jgalego/mote/internal/task"
	"github.com/jgalego/mote/internal/ui"
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
Tab completes commands, tasks, {{names}} and file names; the arrow keys move and recall earlier cells.
  /tasks  what a cell can run   /examples  cells to try   /vars  what you have bound   /cells  what you have run
  /note TEXT  add a paragraph of text   /embed FILE  add an image, audio or video to the notebook
  /undo  drop the last thing added   /save FILE  keep them in a file   /help  this   /exit  or Ctrl-D
With no file the cells live in memory; on exit you are asked whether to save them.`

// cellCount says how many cells, in words that agree.
func cellCount(n int) string {
	if n == 1 {
		return "1 cell"
	}
	return fmt.Sprintf("%d cells", n)
}

// consoleTagline is the line under the banner when a session opens: where
// the cells are kept, how many there are, and where to find the commands.
func consoleTagline(path string, cells int) string {
	where := path
	if where == "" {
		where = "not saved"
	}
	return fmt.Sprintf("notebook console · %s · %s · /help", where, cellCount(cells))
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
	in     nbInput
	undo   []undoStep // what was added here, latest last
}

// undoStep is what it takes to take back one thing added to the notebook.
type undoStep struct {
	before string // the notebook as it was
	label  string // what was added, in words: "cell 3", "a note"
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
		a: a, path: path, book: book, tasks: tasks, values: book.Values(),
		models: a.newNbModels(vals), vals: vals,
		yes: vals["--yes"] == "true" || vals["-y"] == "true",
	}
	defer s.models.close()

	live := a.tty && a.uo.Live()
	s.in = newPlainInput(a, live)
	if live {
		// At a terminal the line is edited here, not by the terminal, so the
		// arrow keys and Tab do something; a question is read the same way.
		if f, ok := a.in.(*os.File); ok {
			if restore, ok := ui.EnterCbreak(f); ok {
				defer restore()
				s.in = &editorInput{a: a, r: bufio.NewReader(f), complete: s.complete, highlight: s.highlight}
			}
		}
		fmt.Fprint(a.err, a.ue.Banner(consoleTagline(path, len(book.Cells))))
	}
	for {
		prompt := a.ue.Accent(fmt.Sprintf("In [%d]: ", len(s.book.Cells)+1))
		line, ok := s.in.turn(prompt)
		if !ok {
			if err := s.in.err(); err != nil {
				return err
			}
			// Ctrl-D ends the input, and what is asked next is read afresh.
			s.in.resume()
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
			fmt.Fprintln(a.err, a.colourCommands(nbConsoleHelp))
			continue
		case line == "/note" || strings.HasPrefix(line, "/note "):
			err = s.note(strings.TrimPrefix(line, "/note"))
		case line == "/embed" || strings.HasPrefix(line, "/embed "):
			err = s.embed(strings.TrimPrefix(line, "/embed"))
		case line == "/tasks":
			s.listTasks()
			continue
		case line == "/examples":
			s.showExamples()
			continue
		case line == "/vars":
			s.listVars()
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

// listTasks prints what a cell can run, each with the arguments it takes.
func (s *nbSession) listTasks() {
	tasks := cellTasks(s.tasks)
	width := 0
	for _, t := range tasks {
		width = max(width, len(strings.TrimSpace(t.ID+" "+t.Usage())))
	}
	for _, t := range tasks {
		usage := t.Usage()
		head := strings.TrimSpace(t.ID + " " + usage)
		// Pad by the plain text; the colour codes take no columns.
		name := s.a.uo.Bold(t.ID)
		if usage != "" {
			name += " " + s.a.uo.Dim(usage)
		}
		fmt.Fprintf(s.a.out, "%s%s  %s\n", name, strings.Repeat(" ", width-len(head)), t.Summary)
	}
}

// showExamples prints cells to try.
func (s *nbSession) showExamples() {
	for _, e := range consoleExamples {
		fmt.Fprintln(s.a.out, e)
	}
}

// listVars prints what the cells run so far have bound.
func (s *nbSession) listVars() {
	names := make([]string, 0, len(s.values))
	for name := range s.values {
		names = append(names, name)
	}
	if len(names) == 0 {
		fmt.Fprintln(s.a.err, "nothing is bound yet; write NAME = task ... to keep a cell's output as {{NAME}}")
		return
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(s.a.out, "%s = %s\n", name, oneLine(s.values[name]))
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
	s.book, s.values = book, book.Values()
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
	fmt.Fprintf(s.a.err, "%s saved %s in %s\n", s.a.ue.OK(), contents(len(s.book.Cells)), path)
	return nil
}

// finish ends a session. One that lives in memory and ran something is kept
// if saveTo says where, or if you say where when asked; without a terminal
// to ask at, it says what is being dropped rather than dropping it quietly.
func (s *nbSession) finish(saveTo string) error {
	// A session that lives in memory holds exactly what was added to it.
	if s.path != "" || strings.TrimSpace(s.book.String()) == "" {
		return nil
	}
	n := len(s.book.Cells)
	if saveTo != "" {
		return s.save(saveTo)
	}
	if !s.a.tty {
		fmt.Fprintf(s.a.err, "%s %s not saved; pass -o FILE to keep them\n", s.a.ue.Warn(), contents(n))
		return nil
	}
	question := fmt.Sprintf("save the %s you ran to a file? [y/N] ", cellCount(n))
	if n == 0 {
		question = "save your notes to a file? [y/N] "
	}
	if !s.ask(question) {
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

// contents says what a notebook holds, for a message about saving it.
func contents(cells int) string {
	if cells == 0 {
		return "your notes"
	}
	return cellCount(cells)
}

// answer asks a question and returns the reply, "n" if there is none: an
// answer that keeps nothing and asks nothing more.
func (s *nbSession) answer(prompt string) string {
	line, ok := s.in.line(prompt)
	if !ok {
		return "n"
	}
	return line
}

// ask is answer for a question with a yes or a no.
func (s *nbSession) ask(prompt string) bool {
	l := strings.ToLower(strings.TrimSpace(s.answer(prompt)))
	return l == "y" || l == "yes"
}

func (s *nbSession) undoLast() error {
	if len(s.undo) == 0 {
		return errors.New("nothing to undo; only what was added in this session can be dropped")
	}
	step := s.undo[len(s.undo)-1]
	if err := s.restore(step.before); err != nil {
		return err
	}
	s.undo = s.undo[:len(s.undo)-1]
	if err := s.flush(); err != nil {
		return err
	}
	fmt.Fprintf(s.a.err, "%s dropped %s\n", s.a.ue.OK(), step.label)
	return nil
}

// addProse puts a paragraph of Markdown in the notebook. label says what it
// is, for the messages about it.
func (s *nbSession) addProse(text, label string) error {
	before := s.book.String()
	if err := s.book.AppendProse(text); err != nil {
		return err
	}
	if err := s.flush(); err != nil {
		if rerr := s.restore(before); rerr != nil {
			return rerr
		}
		return err
	}
	s.undo = append(s.undo, undoStep{before, label})
	fmt.Fprintf(s.a.err, "%s added %s\n", s.a.ue.OK(), label)
	return nil
}

// note adds text.
func (s *nbSession) note(text string) error {
	// What separates the command from the text is not part of it; the lines
	// after the first keep their indent.
	text = strings.TrimLeft(text, " \t")
	if strings.TrimSpace(text) == "" {
		return errors.New("usage: /note TEXT")
	}
	return s.addProse(text, "a note")
}

// embed adds the files it is given, each shown the way its kind is: an image
// as an image, audio and video with the controls of a player.
func (s *nbSession) embed(args string) error {
	files, err := splitArgs(args)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("usage: /embed FILE...")
	}
	marks := make([]string, len(files))
	for i, f := range files {
		if marks[i], err = embedMarkup(s.embedPath(f), f); err != nil {
			return err
		}
		if err := checkEmbeddable(f); err != nil {
			return err
		}
	}
	label := "an embed of " + strings.Join(files, ", ")
	return s.addProse(strings.Join(marks, "\n\n"), label)
}

// run adds a cell, runs it and prints what it produced. A cell that cannot
// be added or that fails is not kept: the notebook is left as it was, so it
// holds only cells with an output to show.
func (s *nbSession) run(ctx context.Context, line string) error {
	name, expr := motebook.SplitBinding(line)
	if err := s.unknownTask(expr); err != nil {
		return err
	}
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
		return fail(err)
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
	started := time.Now()
	out, err := s.a.runToOutput(ctx, cell, motebook.Key(cell.cell, inputs), s.values, s.vals, s.models)
	s.a.cellDone(fmt.Sprintf("cell %d", idx+1), started, err == nil)
	if err != nil {
		return fail(err)
	}
	s.book.Set(idx, out)
	if err := s.flush(); err != nil {
		return fail(err)
	}
	s.undo = append(s.undo, undoStep{before, fmt.Sprintf("cell %d", idx+1)})
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
	if !s.ask(fmt.Sprintf("%s run %s? [y/N] ", s.a.ue.Warn(), s.a.ue.Bold(oneLine(c.cell.Expr)))) {
		return errors.New("not run")
	}
	return nil
}
