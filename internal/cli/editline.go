package cli

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// slashCommands are the commands recognized at the chat prompt; Tab
// completes them there.
var slashCommands = []string{"/exit", "/quit", "/bye", "/new", "/reset", "/clear", "/help", "/?"}

// completer proposes what the word at pos in line could become: it runs from
// start to end, which is pos unless the word goes on past the cursor, and
// cands are the whole replacements for line[start:end], each with any
// suffix it should carry.
type completer func(line []rune, pos int) (start, end int, cands []string)

// lineOpts says what a line editor offers besides typing.
type lineOpts struct {
	complete  completer // Tab; nil for none
	history   *[]string // Up and Down; nil for none
	highlight highlighter
}

// highlighter colours the line as it is drawn. It may add colour codes and
// nothing else: what is left when they are removed must be the line, since
// the cursor is placed by counting its characters.
type highlighter func(line []rune) string

// slashCompleter completes a command at the start of a line.
func slashCompleter(cmds []string) completer {
	return func(line []rune, pos int) (int, int, []string) {
		typed := string(line[:pos])
		if !strings.HasPrefix(typed, "/") || strings.ContainsAny(typed, " \t") {
			return 0, pos, nil
		}
		return 0, pos, filterPrefix(cmds, typed)
	}
}

// editTurn reads one turn from a terminal already in cbreak mode, echoing
// what is typed and completing a leading "/" command on Tab. Like readTurn,
// a line ending in "\" continues onto the next; ok is false at Ctrl-D on an
// empty line or a read error.
func (a *app) editTurn(r *bufio.Reader) (string, bool) {
	return a.editTurnWith(r, a.ue.Accent("› "), lineOpts{complete: slashCompleter(slashCommands)})
}

// editTurnWith is editTurn with its own prompt and options. A turn of one
// line is added to the history.
func (a *app) editTurnWith(r *bufio.Reader, first string, o lineOpts) (string, bool) {
	var parts []string
	for {
		prompt := first
		if len(parts) > 0 {
			prompt = a.ue.Dim("… ")
		}
		fmt.Fprint(a.err, prompt)
		line, ok := a.readLine(r, prompt, o)
		if !ok {
			if len(parts) > 0 {
				// Ctrl-D on a continuation line ends the turn there.
				fmt.Fprintln(a.err)
				return strings.TrimSpace(strings.Join(parts, "\n")), true
			}
			return "", false
		}
		if strings.HasSuffix(line, "\\") {
			parts = append(parts, strings.TrimSuffix(line, "\\"))
			continue
		}
		parts = append(parts, line)
		turn := strings.TrimSpace(strings.Join(parts, "\n"))
		if o.history != nil && turn != "" && !strings.Contains(turn, "\n") {
			if h := *o.history; len(h) == 0 || h[len(h)-1] != turn {
				*o.history = append(h, turn)
			}
		}
		return turn, true
	}
}

// editLine reads one line with the chat prompt's editing: see readLine.
func (a *app) editLine(r *bufio.Reader, prompt string) (string, bool) {
	return a.readLine(r, prompt, lineOpts{complete: slashCompleter(slashCommands)})
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// visibleLen is how many columns s takes on a terminal, colour codes
// excluded.
func visibleLen(s string) int {
	n := 0
	for _, r := range ansiRe.ReplaceAllString(s, "") {
		n += runeWidth(r)
	}
	return n
}

// runeWidth is how many columns a terminal gives a character: two for the
// wide ones of East Asian scripts and for emoji, none for a mark that
// combines with the one before it, one for the rest.
func runeWidth(r rune) int {
	switch {
	case r == 0 || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r):
		return 0
	case r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) ||
		(r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe30 && r <= 0xfe6f) ||
		(r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x1f300 && r <= 0x1f64f) ||
		(r >= 0x1f900 && r <= 0x1f9ff) ||
		(r >= 0x20000 && r <= 0x3fffd)):
		return 2
	}
	return 1
}

// editor is a line being edited on a terminal in cbreak mode, which echoes
// nothing and buffers nothing, so drawing it is up to us.
type editor struct {
	a      *app
	r      *bufio.Reader
	prompt string
	o      lineOpts
	width  int // columns
	plen   int // columns the prompt takes

	buf   []rune
	pos   int    // the cursor, as an index into buf
	row   int    // the row of the drawn line the cursor is on, from its first
	hist  int    // which history entry is showing; len(history) is the line being typed
	draft []rune // the line being typed, while an older one shows
}

// readLine reads one line, byte by byte. It edits in place (Left, Right,
// Home, End, Delete, Backspace and the usual Ctrl keys), recalls earlier lines
// with Up and Down, completes with Tab, and takes UTF-8. Any other escape
// sequence is swallowed rather than left to corrupt the line. ok is false at
// Ctrl-D on an empty line or on a read error.
func (a *app) readLine(r *bufio.Reader, prompt string, o lineOpts) (string, bool) {
	e := &editor{a: a, r: r, prompt: prompt, o: o, width: max(a.ue.Width(), 20), plen: visibleLen(prompt)}
	if o.history != nil {
		e.hist = len(*o.history)
	}
	return e.run()
}

func (e *editor) run() (string, bool) {
	for {
		b, err := e.r.ReadByte()
		if err != nil {
			return "", false
		}
		switch {
		case b == '\r' || b == '\n':
			e.finish()
			return string(e.buf), true
		case b == 4: // Ctrl-D
			if len(e.buf) == 0 {
				return "", false
			}
			e.deleteAt(e.pos)
		case b == 127 || b == 8: // backspace
			if e.pos > 0 {
				e.pos--
				e.deleteAt(e.pos)
			}
		case b == '\t':
			e.tab()
		case b == 1: // Ctrl-A
			e.moveTo(0)
		case b == 5: // Ctrl-E
			e.moveTo(len(e.buf))
		case b == 2: // Ctrl-B
			e.moveTo(e.pos - 1)
		case b == 6: // Ctrl-F
			e.moveTo(e.pos + 1)
		case b == 16: // Ctrl-P
			e.recall(-1)
		case b == 14: // Ctrl-N
			e.recall(1)
		case b == 21: // Ctrl-U: everything before the cursor
			e.buf, e.pos = e.buf[e.pos:], 0
			e.draw()
		case b == 11: // Ctrl-K: everything after it
			e.buf = e.buf[:e.pos]
			e.draw()
		case b == 23: // Ctrl-W: the word before it
			start := e.wordStart(e.pos)
			e.buf = append(e.buf[:start:start], e.buf[e.pos:]...)
			e.pos = start
			e.draw()
		case b == 0x1b:
			e.escape()
		case b >= 0x20 && b < 0x7f:
			e.insert(rune(b))
		case b >= 0x80:
			e.r.UnreadByte()
			if c, _, err := e.r.ReadRune(); err == nil && c != utf8.RuneError {
				e.insert(c)
			}
		}
	}
}

// insert types c at the cursor.
func (e *editor) insert(c rune) {
	atEnd := e.pos == len(e.buf)
	e.buf = append(e.buf, 0)
	copy(e.buf[e.pos+1:], e.buf[e.pos:])
	e.buf[e.pos] = c
	e.pos++
	if !atEnd {
		e.draw()
		return
	}
	// What was typed may change how the rest is coloured, and a character
	// that is not one column wide may wrap where the terminal says.
	if e.o.highlight != nil || runeWidth(c) != 1 {
		e.draw()
		return
	}
	// Typing at the end needs no redraw; the terminal wraps by itself, and
	// only the exact end of a row needs the line moved down for us.
	fmt.Fprint(e.a.err, string(c))
	row, _, full := e.at(len(e.buf))
	if full {
		fmt.Fprint(e.a.err, "\r\n")
	}
	e.row = row
}

// at is where the character at index i of the line starts on the screen, as
// a row counted from the prompt's and a column; for i past the end, it is
// where the cursor is after the line. full reports a line that ends exactly
// at the edge, where a terminal holds the cursor on the last column until
// something else is written, so the move to the next row is ours to make.
func (e *editor) at(i int) (row, col int, full bool) {
	row, col = e.plen/e.width, e.plen%e.width
	for j, r := range e.buf {
		w := runeWidth(r)
		if col+w > e.width { // it does not fit: it goes on the next row
			row, col = row+1, 0
		}
		if j == i {
			return row, col, false
		}
		col += w
		full = col == e.width
		if full {
			row, col = row+1, 0
		}
	}
	return row, col, full
}

func (e *editor) deleteAt(i int) {
	if i >= len(e.buf) {
		return
	}
	e.buf = append(e.buf[:i], e.buf[i+1:]...)
	e.draw()
}

func (e *editor) moveTo(i int) {
	i = min(max(i, 0), len(e.buf))
	if i == e.pos {
		return
	}
	e.pos = i
	e.draw()
}

// wordStart is where the word before i begins.
func (e *editor) wordStart(i int) int {
	for i > 0 && e.buf[i-1] == ' ' {
		i--
	}
	for i > 0 && e.buf[i-1] != ' ' {
		i--
	}
	return i
}

// wordEnd is where the word at or after i ends.
func (e *editor) wordEnd(i int) int {
	for i < len(e.buf) && e.buf[i] == ' ' {
		i++
	}
	for i < len(e.buf) && e.buf[i] != ' ' {
		i++
	}
	return i
}

// recall shows an earlier (-1) or later (+1) line from the history. The line
// being typed is kept, and comes back after the newest.
func (e *editor) recall(dir int) {
	if e.o.history == nil {
		return
	}
	h := *e.o.history
	next := e.hist + dir
	if next < 0 || next > len(h) {
		return
	}
	if e.hist == len(h) {
		e.draft = append([]rune(nil), e.buf...)
	}
	e.hist = next
	if next == len(h) {
		e.buf = append([]rune(nil), e.draft...)
	} else {
		e.buf = []rune(h[next])
	}
	e.pos = len(e.buf)
	e.draw()
}

// escape reads an escape sequence and does what it asks for, if it is a key
// this understands. Anything else is read to its end and dropped.
func (e *editor) escape() {
	b, err := e.r.ReadByte()
	if err != nil {
		return
	}
	if b != '[' && b != 'O' {
		// Esc on its own, or with Alt: what follows is a key of its own.
		e.r.UnreadByte()
		return
	}
	var params []byte
	for {
		c, err := e.r.ReadByte()
		if err != nil {
			return
		}
		if c >= 0x40 && c <= 0x7e {
			e.key(string(params), c)
			return
		}
		params = append(params, c)
	}
}

// key acts on the arrow, Home, End and Delete keys. A held Ctrl (";5") makes
// Left and Right move by words.
func (e *editor) key(params string, final byte) {
	word := strings.HasSuffix(params, ";5")
	switch final {
	case 'A':
		e.recall(-1)
	case 'B':
		e.recall(1)
	case 'C':
		if word {
			e.moveTo(e.wordEnd(e.pos))
		} else {
			e.moveTo(e.pos + 1)
		}
	case 'D':
		if word {
			e.moveTo(e.wordStart(e.pos))
		} else {
			e.moveTo(e.pos - 1)
		}
	case 'H':
		e.moveTo(0)
	case 'F':
		e.moveTo(len(e.buf))
	case '~':
		switch params {
		case "1", "7":
			e.moveTo(0)
		case "4", "8":
			e.moveTo(len(e.buf))
		case "3":
			e.deleteAt(e.pos)
		}
	}
}

// draw redraws the whole line, wherever it wraps, and puts the cursor back
// where it is in it.
func (e *editor) draw() {
	w := e.a.err
	if e.row > 0 {
		fmt.Fprintf(w, "\x1b[%dA", e.row)
	}
	shown := string(e.buf)
	if e.o.highlight != nil {
		shown = e.o.highlight(e.buf)
	}
	fmt.Fprintf(w, "\r\x1b[J%s%s", e.prompt, shown)
	endRow, _, full := e.at(len(e.buf))
	if full {
		fmt.Fprint(w, "\r\n") // the cursor waits at the edge; move it to the next row for real
	}
	row, col, _ := e.at(e.pos)
	if up := endRow - row; up > 0 {
		fmt.Fprintf(w, "\x1b[%dA", up)
	}
	fmt.Fprint(w, "\r")
	if col > 0 {
		fmt.Fprintf(w, "\x1b[%dC", col)
	}
	e.row = row
}

// finish ends the line: the cursor goes below it, wherever it was in it.
func (e *editor) finish() {
	endRow, _, full := e.at(len(e.buf))
	if down := endRow - e.row; down > 0 {
		fmt.Fprintf(e.a.err, "\x1b[%dB", down)
	}
	if full {
		fmt.Fprint(e.a.err, "\r") // the line filled its last row: the next one is already free
		return
	}
	fmt.Fprintln(e.a.err)
}

// tab completes the word before the cursor: a single match replaces it, an
// ambiguous one extends it to what the matches share, and if that adds
// nothing, the candidates print below for the line to be drawn under.
func (e *editor) tab() {
	if e.o.complete == nil {
		return
	}
	start, end, cands := e.o.complete(e.buf, e.pos)
	if len(cands) == 0 {
		return
	}
	typed := string(e.buf[start:e.pos])
	insert := ""
	switch {
	case len(cands) == 1:
		insert = cands[0]
	case len(commonPrefix(cands)) > len(typed):
		insert = commonPrefix(cands)
	default:
		e.finish()
		fmt.Fprintln(e.a.err, strings.Join(cands, "  "))
		e.row = 0
		e.draw()
		return
	}
	tail := append([]rune(nil), e.buf[end:]...)
	e.buf = append(append(e.buf[:start:start], []rune(insert)...), tail...)
	e.pos = start + utf8.RuneCountInString(insert)
	e.draw()
}

// commonPrefix returns the longest prefix shared by every string in ss.
func commonPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	p := []rune(ss[0])
	for _, s := range ss[1:] {
		r := []rune(s)
		n := 0
		for n < len(p) && n < len(r) && p[n] == r[n] {
			n++
		}
		p = p[:n]
	}
	return string(p)
}
