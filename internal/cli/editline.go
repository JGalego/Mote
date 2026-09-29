package cli

import (
	"bufio"
	"fmt"
	"strings"
)

// slashCommands are the commands recognized at the chat prompt; Tab
// completes them there.
var slashCommands = []string{"/exit", "/quit", "/bye", "/new", "/reset", "/clear", "/help", "/?"}

// editTurn reads one turn from a terminal already in cbreak mode, echoing
// what is typed and completing a leading "/" command on Tab. Like readTurn,
// a line ending in "\" continues onto the next; ok is false at Ctrl-D on an
// empty line or a read error.
func (a *app) editTurn(r *bufio.Reader) (string, bool) {
	var parts []string
	for {
		prompt := a.ue.Accent("› ")
		if len(parts) > 0 {
			prompt = a.ue.Dim("… ")
		}
		fmt.Fprint(a.err, prompt)
		line, ok := a.editLine(r, prompt)
		if !ok {
			if len(parts) > 0 {
				return strings.Join(parts, "\n"), true
			}
			return "", false
		}
		if strings.HasSuffix(line, "\\") {
			parts = append(parts, strings.TrimSuffix(line, "\\"))
			continue
		}
		parts = append(parts, line)
		return strings.TrimSpace(strings.Join(parts, "\n")), true
	}
}

// editLine reads one line byte by byte, since the terminal no longer
// echoes or buffers it. Backspace edits, Tab completes a leading slash
// command, and an escape sequence (an arrow key, say) is swallowed rather
// than left to corrupt the buffer.
func (a *app) editLine(r *bufio.Reader, prompt string) (string, bool) {
	var buf []rune
	redraw := func() { fmt.Fprintf(a.err, "\r\x1b[K%s%s", prompt, string(buf)) }
	for {
		b, err := r.ReadByte()
		if err != nil {
			return "", false
		}
		switch {
		case b == '\r' || b == '\n':
			fmt.Fprintln(a.err)
			return string(buf), true
		case b == 4: // Ctrl-D
			if len(buf) == 0 {
				return "", false
			}
		case b == 127 || b == 8: // backspace/DEL
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
				fmt.Fprint(a.err, "\b \b")
			}
		case b == '\t':
			buf = a.completeSlash(buf, redraw)
		case b == 0x1b:
			skipEscape(r)
		case b >= 0x20 && b < 0x7f:
			buf = append(buf, rune(b))
			fmt.Fprintf(a.err, "%c", b)
		}
	}
}

// completeSlash expands buf on Tab: a single match replaces it outright, an
// ambiguous one extends buf to their common prefix, and if that adds
// nothing, the candidates print below for the line to be redrawn under.
func (a *app) completeSlash(buf []rune, redraw func()) []rune {
	cur := string(buf)
	if !strings.HasPrefix(cur, "/") {
		return buf
	}
	matches := filterPrefix(slashCommands, cur)
	switch len(matches) {
	case 0:
		return buf
	case 1:
		buf = []rune(matches[0])
	default:
		if common := commonPrefix(matches); len(common) > len(cur) {
			buf = []rune(common)
		} else {
			fmt.Fprintln(a.err)
			fmt.Fprintln(a.err, strings.Join(matches, "  "))
		}
	}
	redraw()
	return buf
}

// commonPrefix returns the longest prefix shared by every string in ss.
func commonPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	p := ss[0]
	for _, s := range ss[1:] {
		for !strings.HasPrefix(s, p) {
			p = p[:len(p)-1]
		}
	}
	return p
}

// skipEscape discards the rest of an escape sequence (an arrow key, a
// function key) so it cannot leak literal bytes into the line.
func skipEscape(r *bufio.Reader) {
	b, err := r.ReadByte()
	if err != nil || (b != '[' && b != 'O') {
		return
	}
	for {
		b, err := r.ReadByte()
		if err != nil || (b >= 0x40 && b <= 0x7e) {
			return
		}
	}
}
