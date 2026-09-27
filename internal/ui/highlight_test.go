package ui

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// colourUI returns a UI that paints into buf without needing a terminal.
func colourUI(buf *bytes.Buffer) *UI { return &UI{w: buf, color: true} }

const pySrc = `import os

def greet(name):
    """Say hello.

    Spans several lines.
    """
    return f"hi {name} {os.getpid()}"
`

func TestHighlightKeepsTextAndAddsColour(t *testing.T) {
	u := colourUI(&bytes.Buffer{})
	got := u.Highlight(pySrc, "python")
	if !strings.Contains(got, "\x1b[") {
		t.Fatal("no escape sequences in highlighted code")
	}
	if plain := ansi.ReplaceAllString(got, ""); plain != pySrc {
		t.Errorf("highlighting changed the text:\n got %q\nwant %q", plain, pySrc)
	}
}

func TestHighlightPlainUIIsByteIdentical(t *testing.T) {
	u := Plain(&bytes.Buffer{})
	if got := u.Highlight(pySrc, "python"); got != pySrc {
		t.Errorf("plain UI highlighted: %q", got)
	}
	if got := u.Highlight("x := 1\n", ""); got != "x := 1\n" {
		t.Errorf("plain UI highlighted: %q", got)
	}
}

func TestHighlightUnknownLanguageFallsBack(t *testing.T) {
	u := colourUI(&bytes.Buffer{})
	const src = "!!! not a language !!!\n"
	if got := ansi.ReplaceAllString(u.Highlight(src, "nosuchlang"), ""); got != src {
		t.Errorf("text changed: %q", got)
	}
}

// Every line must carry its own escapes: a multi-line string that starts on
// one line and ends three lines later must not leave colour switched on, or
// the rest of the terminal takes on its colour.
func TestHighlightLinesAreSelfContained(t *testing.T) {
	u := colourUI(&bytes.Buffer{})
	for i, l := range u.highlightLines(pySrc, "python") {
		codes := ansi.FindAllString(l, -1)
		if len(codes) == 0 {
			continue
		}
		if last := codes[len(codes)-1]; last != "\x1b[0m" {
			t.Errorf("line %d ends with colour still on (%q): %q", i, last, l)
		}
	}
}

func TestCodeStreamWritesWholeLinesAsTheyArrive(t *testing.T) {
	var buf bytes.Buffer
	u := colourUI(&buf)
	c := u.CodeStream("python")

	// No trailing newline: the last line stays buffered until Flush, the way
	// a model's final token does.
	src := strings.TrimSuffix(pySrc, "\n")
	for i := 0; i < len(src); i += 7 { // arbitrary chunking, as tokens arrive
		c.Write(src[i:min(i+7, len(src))])
	}
	got := ansi.ReplaceAllString(buf.String(), "")
	want := src[:strings.LastIndexByte(src, '\n')+1]
	if got != want {
		t.Errorf("before flush:\n got %q\nwant %q", got, want)
	}
	c.Flush()
	if got := ansi.ReplaceAllString(buf.String(), ""); got != src {
		t.Errorf("after flush:\n got %q\nwant %q", got, src)
	}
}

func TestCodeStreamCompleteTextNeedsNoFlush(t *testing.T) {
	var buf bytes.Buffer
	c := colourUI(&buf).CodeStream("python")
	c.Write(pySrc)
	if got := ansi.ReplaceAllString(buf.String(), ""); got != pySrc {
		t.Errorf("streamed text differs:\n got %q\nwant %q", got, pySrc)
	}
	c.Flush()
	if got := ansi.ReplaceAllString(buf.String(), ""); got != pySrc {
		t.Errorf("flush added output: %q", got)
	}
}

func TestCodeStreamNilIsNoOp(t *testing.T) {
	var buf bytes.Buffer
	c := Plain(&buf).CodeStream("python")
	if c != nil {
		t.Fatal("colourless UI returned a highlighter")
	}
	c.Lang("go")
	c.Write("x := 1\n")
	c.Flush()
	if buf.Len() != 0 {
		t.Errorf("nil stream wrote %q", buf.String())
	}
}

func TestCodeStreamLangOnlyBeforeOutput(t *testing.T) {
	var buf bytes.Buffer
	c := colourUI(&buf).CodeStream("")
	c.Lang("python")
	if c.lang != "python" {
		t.Fatalf("lang not set: %q", c.lang)
	}
	c.Write("import os\n")
	c.Lang("go")
	if c.lang != "python" {
		t.Errorf("lang changed after output started: %q", c.lang)
	}
}

func TestLangForFile(t *testing.T) {
	for file, want := range map[string]string{"a/b/slugify.py": "python", "main.go": "go", "notes": "", "Dockerfile": "docker"} {
		if got := LangForFile(file); got != want {
			t.Errorf("%s: %q, want %q", file, got, want)
		}
	}
}
