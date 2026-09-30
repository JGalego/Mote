package cli

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/jgalego/mote/internal/ui"
)

func testApp() (*app, *bytes.Buffer) {
	var out bytes.Buffer
	return &app{ue: ui.New(io.Discard), err: &out}, &out
}

// read runs the editor over the bytes a terminal would send.
func read(input string, o lineOpts) (string, bool, string) {
	a, out := testApp()
	line, ok := a.readLine(bufio.NewReader(strings.NewReader(input)), "> ", o)
	return line, ok, out.String()
}

const (
	up, down, right, left = "\x1b[A", "\x1b[B", "\x1b[C", "\x1b[D"
	home, end, del        = "\x1b[H", "\x1b[F", "\x1b[3~"
	ctrlLeft, ctrlRight   = "\x1b[1;5D", "\x1b[1;5C"
)

func TestEditLineTypesAndBackspaces(t *testing.T) {
	a, _ := testApp()
	// "hell", backspace erases the second "l", then "lo" finishes "hello".
	line, ok := a.editLine(bufio.NewReader(strings.NewReader("hell\blo\r")), "> ")
	if !ok || line != "hello" {
		t.Errorf("got %q %v, want %q true", line, ok, "hello")
	}
}

func TestEditLineCtrlD(t *testing.T) {
	a, _ := testApp()
	// Ctrl-D on an empty buffer ends the line.
	if _, ok := a.editLine(bufio.NewReader(strings.NewReader("\x04")), "> "); ok {
		t.Error("Ctrl-D on an empty buffer should report no line")
	}
	// Ctrl-D at the end of a line deletes nothing; the line still ends on Enter.
	line, ok := a.editLine(bufio.NewReader(strings.NewReader("hi\x04\r")), "> ")
	if !ok || line != "hi" {
		t.Errorf("got %q %v, want %q true", line, ok, "hi")
	}
	// In the middle of one it deletes the character under the cursor.
	if line, _, _ := read("abc"+left+left+"\x04\r", lineOpts{}); line != "ac" {
		t.Errorf("Ctrl-D mid-line: %q, want %q", line, "ac")
	}
}

func TestEditLineEndsAtEndOfInput(t *testing.T) {
	if line, ok, _ := read("abc", lineOpts{}); ok {
		t.Errorf("a line that was never ended came back: %q", line)
	}
}

func TestEditLineMovesTheCursor(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{"left then insert", "ab" + left + "X\r", "aXb"},
		{"right stops at the end", "ab" + right + right + right + "X\r", "abX"},
		{"left stops at the start", "ab" + left + left + left + "X\r", "Xab"},
		{"home", "abc" + home + "X\r", "Xabc"},
		{"end", "abc" + home + end + "X\r", "abcX"},
		{"ctrl-a and ctrl-e", "abc\x01X\x05Y\r", "XabcY"},
		{"ctrl-b and ctrl-f", "abc\x02\x02\x06X\r", "abXc"},
		{"delete key", "abc" + home + del + "\r", "bc"},
		{"delete key at the end", "abc" + del + "\r", "abc"},
		{"backspace mid-line", "abc" + left + "\b\r", "ac"},
		{"backspace at the start", "abc" + home + "\b\r", "abc"},
		{"ctrl-u", "abc def" + left + left + left + "\x15\r", "def"},
		{"ctrl-k", "abc def" + left + left + left + "\x0b\r", "abc "},
		{"ctrl-w", "one two three\x17\r", "one two "},
		{"ctrl-w twice", "one two three\x17\x17\r", "one "},
		{"word left", "one two" + ctrlLeft + "X\r", "one Xtwo"},
		{"word right", "one two" + home + ctrlRight + "X\r", "oneX two"},
		{"application-mode arrows", "ab\x1bOD" + "X\r", "aXb"},
		{"other escapes are dropped", "a\x1b[15~\x1b[1;2Fb\x1bxc\r", "abc"},
		{"utf-8", "café" + left + "X\r", "cafXé"},
		{"utf-8 backspace", "café\b\r", "caf"},
		{"control keys are not typed", "a\x00\x03b\r", "ab"},
	}
	for _, c := range cases {
		if got, ok, _ := read(c.input, lineOpts{}); !ok || got != c.want {
			t.Errorf("%s: got %q %v, want %q", c.name, got, ok, c.want)
		}
	}
}

func TestEditLineSkipsEscapeSequences(t *testing.T) {
	// An arrow key with nothing to recall must not appear in the line.
	if line, ok, _ := read("a\x1b[Ab\r", lineOpts{}); !ok || line != "ab" {
		t.Errorf("got %q %v, want %q true", line, ok, "ab")
	}
}

func TestEditLineRecallsHistory(t *testing.T) {
	hist := []string{"first", "second"}
	o := lineOpts{history: &hist}
	cases := []struct {
		name, input, want string
	}{
		{"up shows the latest", up + "\r", "second"},
		{"up twice", up + up + "\r", "first"},
		{"up stops at the oldest", up + up + up + up + "\r", "first"},
		{"down comes back", up + up + down + "\r", "second"},
		{"down past the newest restores what was typed", "dra" + up + up + down + down + "\r", "dra"},
		{"down with nothing recalled", "x" + down + "\r", "x"},
		{"a recalled line can be edited", up + left + "X\r", "seconXd"},
		{"ctrl-p and ctrl-n", "\x10\x10\x0e\r", "second"},
	}
	for _, c := range cases {
		if got, ok, _ := read(c.input, o); !ok || got != c.want {
			t.Errorf("%s: got %q %v, want %q", c.name, got, ok, c.want)
		}
	}
	// With no history the keys do nothing.
	if got, _, _ := read("a"+up+down+"b\r", lineOpts{}); got != "ab" {
		t.Errorf("no history: %q", got)
	}
}

func TestEditTurnRemembersWhatWasEntered(t *testing.T) {
	a, _ := testApp()
	var hist []string
	o := lineOpts{history: &hist}
	r := bufio.NewReader(strings.NewReader("one\r\r  \rone\rtwo\rmulti \\\rline\r"))
	for range 5 {
		a.editTurnWith(r, "> ", o)
	}
	// Blank lines, repeats of the last entry and turns of several lines are
	// not kept.
	if got := strings.Join(hist, "|"); got != "one|two" {
		t.Errorf("history %q, want one|two", got)
	}
}

func TestSlashCompleter(t *testing.T) {
	complete := slashCompleter(slashCommands)
	for _, c := range []struct {
		line string
		want string
	}{
		{"/ex", "/exit"},
		{"/e", "/exit"},
		{"/", "/exit,/quit,/bye,/new,/reset,/clear,/help,/?"},
		{"hello", ""},
		{"/exit now", ""},
		{"/zzz", ""},
	} {
		if _, got := complete([]rune(c.line), len(c.line)); strings.Join(got, ",") != c.want {
			t.Errorf("%q: %v, want %q", c.line, got, c.want)
		}
	}
}

func TestEditLineCompletesOnTab(t *testing.T) {
	o := lineOpts{complete: slashCompleter(slashCommands)}
	// A single match is completed outright.
	if line, ok, _ := read("/ex\t\r", o); !ok || line != "/exit" {
		t.Errorf("got %q %v, want %q true", line, ok, "/exit")
	}
	// Non-slash input is left alone, and nothing is drawn for it.
	if line, _, out := read("hello\t\r", o); line != "hello" || strings.Contains(out, "\x1b[J") {
		t.Errorf("got %q, drew %q", line, out)
	}
	// A bare "/" matches everything and has no common prefix: the buffer is
	// unchanged and the candidates are listed.
	line, _, out := read("/\t\r", o)
	if line != "/" || !strings.Contains(out, "/exit") || !strings.Contains(out, "/help") {
		t.Errorf("got %q listing %q", line, out)
	}
	// An ambiguous match is extended to what the candidates share.
	two := lineOpts{complete: func(line []rune, pos int) (int, []string) { return 0, []string{"chat-a", "chat-b"} }}
	if line, _, out := read("ch\t\r", two); line != "chat-" || strings.Contains(out, "chat-a  chat-b") {
		t.Errorf("common prefix: got %q listing %q", line, out)
	}
	// Completion replaces the word before the cursor and keeps what follows.
	word := lineOpts{complete: func(line []rune, pos int) (int, []string) { return 2, []string{"complete"} }}
	if line, _, _ := read("a co rest"+strings.Repeat(left, 5)+"\t\r", word); line != "a complete rest" {
		t.Errorf("mid-line completion: %q", line)
	}
}

func TestCommonPrefix(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"/exit", "/exit"}, "/exit"},
		{[]string{"/new", "/reset", "/clear"}, "/"},
		{[]string{"/exit", "/quit", "/bye"}, "/"},
		{[]string{"café", "cafè"}, "caf"}, // never half a character
		{nil, ""},
	}
	for _, c := range cases {
		if got := commonPrefix(c.in); got != c.want {
			t.Errorf("commonPrefix(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEditorDrawsWrappedLines(t *testing.T) {
	a, out := testApp()
	// A 10-column terminal, a 2-column prompt: 8 characters fill the first row.
	e := &editor{a: a, r: bufio.NewReader(strings.NewReader("")), prompt: "> ", width: 10, plen: 2}
	e.buf = []rune("abcdefghijklmnopq") // 17 characters: 2 + 17 = 19 columns, two rows
	e.pos = len(e.buf)
	e.draw()
	if e.row != 1 {
		t.Errorf("the cursor at the end of a 19-column line is on row %d, want 1", e.row)
	}
	// Move to the start: back up to the first row, at the prompt.
	out.Reset()
	e.pos = 0
	e.draw()
	got := out.String()
	if !strings.HasPrefix(got, "\x1b[1A\r\x1b[J> abcdefghijklmnopq") || !strings.HasSuffix(got, "\x1b[1A\r\x1b[2C") || e.row != 0 {
		t.Errorf("redraw from row 1 to the start: %q (row %d)", got, e.row)
	}
	// A line that fills its row exactly moves the cursor to the next one.
	out.Reset()
	e.buf, e.pos = []rune("abcdefgh"), 8 // 2 + 8 = 10 columns
	e.draw()
	if !strings.Contains(out.String(), "\r\n") || e.row != 1 {
		t.Errorf("a full row: %q (row %d)", out.String(), e.row)
	}
	// Typing across that edge moves down for us.
	out.Reset()
	e2 := &editor{a: a, r: bufio.NewReader(strings.NewReader("")), prompt: "> ", width: 10, plen: 2}
	for _, c := range "abcdefgh" {
		e2.insert(c)
	}
	if !strings.HasSuffix(out.String(), "h\r\n") || e2.row != 1 {
		t.Errorf("typing to the edge: %q (row %d)", out.String(), e2.row)
	}
	// Enter from the top of a wrapped line goes below all of it first.
	out.Reset()
	e.buf, e.pos = []rune("abcdefghijklmnopq"), 0
	e.draw()
	out.Reset()
	e.finish()
	if !strings.HasPrefix(out.String(), "\x1b[1B") || !strings.HasSuffix(out.String(), "\n") {
		t.Errorf("finish: %q", out.String())
	}
}

func TestEditTurnJoinsBackslashContinuations(t *testing.T) {
	a, _ := testApp()
	line, ok := a.editTurn(bufio.NewReader(strings.NewReader("first \\\rsecond\r")))
	if !ok || line != "first \nsecond" {
		t.Errorf("got %q %v, want %q true", line, ok, "first \nsecond")
	}
}

func TestEditTurnEOFMidContinuation(t *testing.T) {
	a, _ := testApp()
	line, ok := a.editTurn(bufio.NewReader(strings.NewReader("first \\\r")))
	if !ok || line != "first " {
		t.Errorf("got %q %v, want %q true", line, ok, "first ")
	}
}

func TestVisibleLenIgnoresColour(t *testing.T) {
	for in, want := range map[string]int{"": 0, "> ": 2, "\x1b[38;5;141mIn [1]: \x1b[0m": 8, "café": 4} {
		if got := visibleLen(in); got != want {
			t.Errorf("visibleLen(%q) = %d, want %d", in, got, want)
		}
	}
}
