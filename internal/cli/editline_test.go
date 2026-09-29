package cli

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/jgalego/mote/internal/ui"
)

func TestEditLineTypesAndBackspaces(t *testing.T) {
	a := &app{ue: ui.New(io.Discard), err: &bytes.Buffer{}}
	// "hell", backspace erases the second "l", then "lo" finishes "hello".
	r := bufio.NewReader(strings.NewReader("hell\blo\r"))
	line, ok := a.editLine(r, "> ")
	if !ok || line != "hello" {
		t.Errorf("got %q %v, want %q true", line, ok, "hello")
	}
}

func TestEditLineCtrlD(t *testing.T) {
	a := &app{ue: ui.New(io.Discard), err: &bytes.Buffer{}}
	// Ctrl-D on an empty buffer ends the line.
	if _, ok := a.editLine(bufio.NewReader(strings.NewReader("\x04")), "> "); ok {
		t.Error("Ctrl-D on an empty buffer should report no line")
	}
	// Ctrl-D mid-buffer is a no-op; the line still ends on Enter.
	line, ok := a.editLine(bufio.NewReader(strings.NewReader("hi\x04\r")), "> ")
	if !ok || line != "hi" {
		t.Errorf("got %q %v, want %q true", line, ok, "hi")
	}
}

func TestEditLineSkipsEscapeSequences(t *testing.T) {
	a := &app{ue: ui.New(io.Discard), err: &bytes.Buffer{}}
	// An arrow key (ESC [ A) between two letters must not appear in the line.
	line, ok := a.editLine(bufio.NewReader(strings.NewReader("a\x1b[Ab\r")), "> ")
	if !ok || line != "ab" {
		t.Errorf("got %q %v, want %q true", line, ok, "ab")
	}
}

func TestCompleteSlashSingleMatch(t *testing.T) {
	a := &app{ue: ui.New(io.Discard), err: &bytes.Buffer{}}
	line, ok := a.editLine(bufio.NewReader(strings.NewReader("/ex\t\r")), "> ")
	if !ok || line != "/exit" {
		t.Errorf("got %q %v, want %q true", line, ok, "/exit")
	}
}

func TestCompleteSlashCommonPrefix(t *testing.T) {
	a := &app{ue: ui.New(io.Discard), err: &bytes.Buffer{}}
	// "/e" -> only /exit starts with it, so one Tab should finish it.
	buf := a.completeSlash([]rune("/e"), func() {})
	if string(buf) != "/exit" {
		t.Errorf("got %q, want %q", string(buf), "/exit")
	}
	// A bare "/" matches everything and has no further common prefix, so
	// Tab should leave the buffer untouched (but list the candidates).
	var out bytes.Buffer
	a.err = &out
	buf = a.completeSlash([]rune("/"), func() {})
	if string(buf) != "/" {
		t.Errorf("got %q, want the buffer unchanged", string(buf))
	}
	if !strings.Contains(out.String(), "/exit") || !strings.Contains(out.String(), "/help") {
		t.Errorf("candidate list missing entries: %q", out.String())
	}
}

func TestCompleteSlashIgnoresNonSlashInput(t *testing.T) {
	a := &app{ue: ui.New(io.Discard), err: &bytes.Buffer{}}
	buf := a.completeSlash([]rune("hello"), func() { t.Error("redraw should not run for non-slash input") })
	if string(buf) != "hello" {
		t.Errorf("got %q, want unchanged %q", string(buf), "hello")
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
		{nil, ""},
	}
	for _, c := range cases {
		if got := commonPrefix(c.in); got != c.want {
			t.Errorf("commonPrefix(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEditTurnJoinsBackslashContinuations(t *testing.T) {
	a := &app{ue: ui.New(io.Discard), err: &bytes.Buffer{}}
	r := bufio.NewReader(strings.NewReader("first \\\rsecond\r"))
	line, ok := a.editTurn(r)
	if !ok || line != "first \nsecond" {
		t.Errorf("got %q %v, want %q true", line, ok, "first \nsecond")
	}
}

func TestEditTurnEOFMidContinuation(t *testing.T) {
	a := &app{ue: ui.New(io.Discard), err: &bytes.Buffer{}}
	r := bufio.NewReader(strings.NewReader("first \\\r"))
	line, ok := a.editTurn(r)
	if !ok || line != "first " {
		t.Errorf("got %q %v, want %q true", line, ok, "first ")
	}
}
