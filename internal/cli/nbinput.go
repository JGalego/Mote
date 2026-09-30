package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jgalego/mote/internal/task"
)

// nbInput is where a console session gets its cells and its answers. At a
// terminal it is an editor that recalls, moves and completes; otherwise it is
// plain lines.
type nbInput interface {
	// turn reads a cell or a command; a line ending in \ continues onto the next.
	turn(prompt string) (string, bool)
	// line reads one line, to answer a question.
	line(prompt string) (string, bool)
	// resume makes reading possible again after the end of input, so that a
	// question can still be asked once Ctrl-D has ended the session.
	resume()
	// err is why input ended, if it was not simply the end.
	err() error
}

// plainInput reads whole lines the terminal, or a pipe, has already edited.
type plainInput struct {
	a    *app
	sc   *bufio.Scanner
	live bool // show the prompt
}

func newPlainInput(a *app, live bool) *plainInput {
	p := &plainInput{a: a, live: live}
	p.resume()
	return p
}

func (p *plainInput) err() error { return p.sc.Err() }

func (p *plainInput) turn(prompt string) (string, bool) {
	return p.a.readTurnPrompt(p.sc, p.live, prompt)
}

func (p *plainInput) line(prompt string) (string, bool) {
	fmt.Fprint(p.a.err, prompt)
	if !p.sc.Scan() {
		fmt.Fprintln(p.a.err)
		return "", false
	}
	return p.sc.Text(), true
}

// resume starts a scanner again: one that has reported the end of input
// never reads any more.
func (p *plainInput) resume() {
	p.sc = bufio.NewScanner(p.a.in)
	p.sc.Buffer(make([]byte, 1<<20), 1<<20)
}

// editorInput reads at a terminal in cbreak mode, through the line editor.
type editorInput struct {
	a         *app
	r         *bufio.Reader
	complete  completer
	highlight highlighter
	history   []string
}

func (e *editorInput) turn(prompt string) (string, bool) {
	line, ok := e.a.editTurnWith(e.r, prompt, lineOpts{complete: e.complete, history: &e.history, highlight: e.highlight})
	if !ok {
		fmt.Fprintln(e.a.err) // Ctrl-D leaves the cursor on the prompt's line
	}
	return line, ok
}

func (e *editorInput) line(prompt string) (string, bool) {
	fmt.Fprint(e.a.err, prompt)
	line, ok := e.a.readLine(e.r, prompt, lineOpts{})
	if !ok {
		fmt.Fprintln(e.a.err)
	}
	return line, ok
}

func (e *editorInput) resume() {}

func (e *editorInput) err() error { return nil }

// consoleCommands are the /commands of a console session, as Tab offers them.
var consoleCommands = []string{"/help", "/?", "/tasks", "/examples", "/cells", "/vars", "/undo", "/save ", "/exit", "/quit", "/bye"}

// consoleExamples are cells to try, shown by /examples.
var consoleExamples = []string{
	`chat "what is the capital of France"`,
	`city = chat "name a landmark in the capital of France"`,
	`chat "how tall is it? {{city}}"`,
	`code "Python function that reverses a string" | explain -`,
	`transcribe talk.mp3 | translate French -`,
	`describe photo.jpg "What is written on the sign?"`,
	`frames clip.mp4 3 | describe -`,
}

// cellTasks are the tasks a cell can run: all but those that need an output
// path, which a cell has no place to give.
func cellTasks(tasks []task.Task) []task.Task {
	var out []task.Task
	for _, t := range tasks {
		if t.Output != "required" {
			out = append(out, t)
		}
	}
	return out
}

// highlight colours a /command as it is typed: cyan while it can still be
// one, red once it cannot, so a mistyped command shows before Enter. Only
// /save takes an argument, so any other command with words after it is red.
func (s *nbSession) highlight(line []rune) string {
	text := string(line)
	if !strings.HasPrefix(text, "/") {
		return text
	}
	end := strings.IndexAny(text, " \t")
	if end < 0 {
		end = len(text)
	}
	cmd, rest := text[:end], text[end:]
	prefix, exact, takesArgs := false, false, false
	for _, known := range consoleCommands {
		if strings.HasPrefix(known, cmd) {
			prefix = true
		}
		if strings.TrimSpace(known) == cmd {
			exact, takesArgs = true, strings.HasSuffix(known, " ")
		}
	}
	if (rest == "" && prefix) || (exact && takesArgs) {
		return s.a.ue.Cyan(cmd) + rest
	}
	return s.a.ue.Red(cmd) + rest
}

// colourCommands colours the /commands in a piece of help text.
func (a *app) colourCommands(text string) string {
	return slashWordRe.ReplaceAllStringFunc(text, func(m string) string {
		i := strings.Index(m, "/")
		return m[:i] + a.ue.Cyan(m[i:])
	})
}

var slashWordRe = regexp.MustCompile(`(^|\s)/[a-z?]+`)

var bindingPrefixRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*=$`)

// stageStart reports whether what precedes a word leaves it in the place a
// task goes: the start of a cell, after a |, or after "name =".
func stageStart(prefix string) bool {
	p := strings.TrimSpace(prefix)
	return p == "" || strings.HasSuffix(p, "|") || bindingPrefixRe.MatchString(p)
}

// complete is what Tab does in a console: it finishes a /command, a task, the
// name of a value in {{ }}, or a file name, according to where the cursor is.
func (s *nbSession) complete(line []rune, pos int) (int, []string) {
	before := string(line[:pos])
	runes := func(byteIdx int) int { return utf8.RuneCountInString(before[:byteIdx]) }

	if strings.HasPrefix(before, "/") {
		return slashCompleter(consoleCommands)(line, pos)
	}
	if i := strings.LastIndex(before, "{{"); i >= 0 && !strings.Contains(before[i:], "}}") {
		typed := before[i+2:]
		var names []string
		for name := range s.values {
			if strings.HasPrefix(name, typed) {
				names = append(names, name+"}}")
			}
		}
		sort.Strings(names)
		return runes(i + 2), names
	}

	ws := strings.LastIndexAny(before, " \t\"'|=") + 1
	word := before[ws:]
	if stageStart(before[:ws]) {
		var ids []string
		for _, t := range cellTasks(s.tasks) {
			if strings.HasPrefix(t.ID, word) {
				ids = append(ids, t.ID+" ")
			}
		}
		if strings.HasPrefix("sh:", word) {
			ids = append(ids, "sh: ")
		}
		sort.Strings(ids)
		return runes(ws), ids
	}
	if word == "" {
		return runes(ws), nil
	}
	quoted := ws > 0 && (before[ws-1] == '"' || before[ws-1] == '\'')
	return runes(ws), completePath(word, quoted)
}

// completePath lists the files and directories that word could be the start
// of. A name with a space in it is offered only inside quotes, where it can
// be written as it is.
func completePath(word string, quoted bool) []string {
	dir, base := filepath.Split(word)
	entries, err := os.ReadDir(filepath.Join(".", dir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base) || (strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".")) {
			continue
		}
		if strings.Contains(name, " ") && !quoted {
			continue
		}
		cand := dir + name
		// A link to a directory is one too.
		if info, err := os.Stat(filepath.Join(".", dir, name)); err == nil && info.IsDir() {
			cand += "/"
		}
		out = append(out, cand)
	}
	sort.Strings(out)
	return out
}

// unknownTask explains a first word that is not a task, which is what a
// sentence typed as if to a person looks like. It returns nil when every
// task in the cell exists, or when the cell does not parse, which is
// reported better where it is run.
func (s *nbSession) unknownTask(expr string) error {
	stages, err := parseStages(expr)
	if err != nil {
		return nil
	}
	for i, st := range stages {
		if st.shell != "" {
			continue
		}
		if _, ok := task.Find(s.tasks, st.id); ok {
			continue
		}
		if i == 0 && len(stages) == 1 {
			return usagef("unknown task %q; a cell is a task, like chat %s (/tasks lists them, /examples shows some)", st.id, pipeQuote(expr))
		}
		return usagef("unknown task %q in stage %d (/tasks lists them)", st.id, i+1)
	}
	return nil
}
