package ui

import (
	"os"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
)

// syntax is mote's highlighting style: foreground colours only, so it sits on
// whatever background the terminal already uses, and close to the palette in
// paint() so highlighted code looks like the rest of the output.
var syntax = chroma.MustNewStyle("mote", chroma.StyleEntries{
	chroma.Comment:             "#6c6c6c",
	chroma.CommentPreproc:      "#af87ff",
	chroma.Keyword:             "bold #af87ff",
	chroma.KeywordType:         "#5fafaf",
	chroma.NameBuiltin:         "#5fafaf",
	chroma.NameClass:           "bold #5fafaf",
	chroma.NameFunction:        "#5fafd7",
	chroma.NameTag:             "bold #af87ff",
	chroma.NameAttribute:       "#5fafd7",
	chroma.NameDecorator:       "#d7875f",
	chroma.LiteralString:       "#5faf5f",
	chroma.LiteralStringEscape: "#d7875f",
	chroma.LiteralNumber:       "#d75f87",
	chroma.Operator:            "#d7875f",
	chroma.GenericDeleted:      "#d75f5f",
	chroma.GenericInserted:     "#5faf5f",
	chroma.GenericHeading:      "bold",
	chroma.GenericSubheading:   "#5fafd7",
	chroma.GenericEmph:         "italic",
	chroma.GenericStrong:       "bold",
	chroma.Error:               "#d75f5f",
})

// lexerFor picks a lexer from a language name, a file extension or, failing
// both, the text itself. It returns nil when nothing matches, which callers
// treat as "print the code unchanged".
func lexerFor(code, lang string) chroma.Lexer {
	var l chroma.Lexer
	if lang = strings.TrimSpace(lang); lang != "" {
		if l = lexers.Get(lang); l == nil {
			l = lexers.Match("f." + strings.TrimPrefix(lang, "."))
		}
	}
	if l == nil {
		l = lexers.Analyse(code)
	}
	if l == nil {
		return nil
	}
	return chroma.Coalesce(l)
}

func formatter() chroma.Formatter {
	switch os.Getenv("COLORTERM") {
	case "truecolor", "24bit":
		return formatters.Get("terminal16m")
	}
	return formatters.Get("terminal256")
}

// highlightLines colours code and returns it one line at a time, without the
// line endings. Each line is formatted on its own so its escape sequences are
// self-contained: colour never bleeds past a newline, which matters when
// lines are written as they arrive. Unhighlightable input comes back as plain
// lines, so callers can always use the result.
func (u *UI) highlightLines(code, lang string) []string {
	plain := strings.Split(code, "\n")
	if !u.color || strings.TrimSpace(code) == "" {
		return plain
	}
	l := lexerFor(code, lang)
	if l == nil {
		return plain
	}
	it, err := l.Tokenise(nil, code)
	if err != nil {
		return plain
	}
	f := formatter()
	var out []string
	for _, toks := range chroma.SplitTokensIntoLines(it.Tokens()) {
		var b strings.Builder
		if err := f.Format(&b, syntax, chroma.Literator(toks...)); err != nil {
			return plain
		}
		out = append(out, strings.TrimRight(b.String(), "\n"))
	}
	if len(out) == 0 {
		return plain
	}
	// A trailing newline ends the last line rather than starting a new one;
	// strings.Split reports the empty line after it, so match that.
	if strings.HasSuffix(code, "\n") {
		out = append(out, "")
	}
	return out
}

// Highlight colours source code for the terminal. lang may be a language
// name ("python"), a file extension (".py" or "py") or empty to guess from
// the code. The code is returned unchanged when colour is off or when no
// lexer fits, so output stays byte-identical in pipes and files.
func (u *UI) Highlight(code, lang string) string {
	return strings.Join(u.highlightLines(code, lang), "\n")
}

// CodeStream highlights code that arrives token by token. Whole lines are
// written as they complete; the partial last line is held until Flush.
//
// Each completed line is produced by re-highlighting everything received so
// far, so constructs that span lines (block comments, triple-quoted strings)
// are coloured with full context instead of line by line. Task output is
// small, so the repeated work is not worth avoiding.
type CodeStream struct {
	u       *UI
	lang    string
	buf     strings.Builder
	printed int // lines already written
}

// CodeStream returns a highlighter writing to u, or nil when colour is off.
// A nil *CodeStream is usable: its methods are no-ops, so callers can fall
// back to writing raw tokens.
func (u *UI) CodeStream(lang string) *CodeStream {
	if !u.color {
		return nil
	}
	return &CodeStream{u: u, lang: lang}
}

// Lang sets the language once it is known, for example from a Markdown fence
// the caller has stripped. It is ignored once output has started.
func (c *CodeStream) Lang(lang string) {
	if c == nil || c.printed > 0 || strings.TrimSpace(lang) == "" {
		return
	}
	c.lang = lang
}

// Write buffers tok and emits the lines it completes.
func (c *CodeStream) Write(tok string) {
	if c == nil {
		return
	}
	c.buf.WriteString(tok)
	if !strings.Contains(tok, "\n") {
		return
	}
	lines := c.u.highlightLines(c.buf.String(), c.lang)
	// The last element is the line still being written.
	for c.printed < len(lines)-1 {
		c.u.Printf("%s\n", lines[c.printed])
		c.printed++
	}
}

// Flush writes whatever is left of the last line.
func (c *CodeStream) Flush() {
	if c == nil || c.buf.Len() == 0 {
		return
	}
	lines := c.u.highlightLines(c.buf.String(), c.lang)
	for ; c.printed < len(lines); c.printed++ {
		if lines[c.printed] != "" {
			c.u.Printf("%s", lines[c.printed])
		}
	}
	c.buf.Reset()
	c.printed = 0
}
