// Package motebook reads notebooks: Markdown files whose ```mote fenced
// blocks are cells, each one a `mote pipe` expression. Prose between cells is
// ignored by the runner and kept as it is.
//
//	```mote as=transcript
//	transcribe meeting.m4a
//	```
//
//	```mote as=summary
//	chat "Summarise in 3 bullets: {{transcript}}"
//	```
//
// A cell with as=NAME binds its output to {{NAME}} for the cells after it.
// After a run the output sits right under its cell, the way a Jupyter
// notebook shows it, in a fence of its own:
//
//	```output key=3fa9c1d2e4b5a678
//	Paris.
//	```
//
// The key fingerprints the cell and the values it read, so a cell whose key
// still matches has nothing new to compute. The package only parses,
// fingerprints and re-renders; running a cell is the CLI's job, because a
// value must be substituted into parsed arguments, never into the pipeline
// text, or a value containing a | or a quote would change its shape.
package motebook

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Cell is one ```mote block.
type Cell struct {
	Index int    // position among the cells, from 0
	Line  int    // line of the opening fence, from 1
	Name  string // the as= binding, empty if the output is not kept
	Expr  string // the pipeline or single task, as written
	Refs  []string

	// Output is what the cell produced the last time it ran, nil if it has
	// not run.
	Output *Output

	closeLine int // line index of the closing fence
	spanEnd   int // line index just past the cell and its output block
}

// Output is the result kept under a cell. An empty Key never matches, which
// is how a cell with side effects is made to run every time.
type Output struct {
	Key  string
	Text string
}

// Book is a parsed notebook.
type Book struct {
	Cells []Cell
	lines []string
}

// bindingRe reads "name = pipeline". A task id or a shell stage never has an
// = after its first word, so a cell cannot be taken for a binding.
var bindingRe = regexp.MustCompile(`(?s)^([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(\S.*)$`)

// SplitBinding separates "name = pipeline", the way a cell is typed where
// there is no as= to write, into its name and its pipeline. Anything else is
// a pipeline with no name.
func SplitBinding(line string) (name, expr string) {
	if m := bindingRe.FindStringSubmatch(line); m != nil {
		return m[1], m[2]
	}
	return "", line
}

var (
	refRe  = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
	nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Parse reads a notebook. It fails on the first problem, naming its line: a
// duplicate or malformed as=, an empty cell, an unclosed fence, or a
// reference to a name no earlier cell binds.
func Parse(src string) (*Book, error) {
	var (
		b       = Book{lines: strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")}
		bound   = map[string]int{} // name -> line of the cell that binds it
		inCell  bool
		other   string // fence marker of a non-mote block we are inside
		cur     Cell
		body    []string
		outOpen = -1 // fence length of the output block being read, if any
		outBody []string
		outLine int
		fresh   bool // the last thing seen was a cell, or blank lines after one
	)
	last := func() *Cell { return &b.Cells[len(b.Cells)-1] }
	for i, raw := range b.lines {
		ln := i + 1
		t := strings.TrimSpace(raw)
		switch {
		case inCell:
			if t == "```" {
				inCell = false
				cur.Expr = strings.TrimSpace(strings.Join(body, "\n"))
				if cur.Expr == "" {
					return nil, fmt.Errorf("line %d: empty cell", cur.Line)
				}
				cur.Refs = Refs(cur.Expr)
				for _, r := range cur.Refs {
					if _, ok := bound[r]; !ok {
						return nil, fmt.Errorf("line %d: {{%s}} is not bound by an earlier cell", cur.Line, r)
					}
				}
				if cur.Name != "" {
					bound[cur.Name] = cur.Line
				}
				cur.Index = len(b.Cells)
				cur.closeLine, cur.spanEnd = i, i+1
				b.Cells = append(b.Cells, cur)
				fresh = true
				continue
			}
			body = append(body, raw)
		case outOpen >= 0:
			if n := fenceLen(t); n >= outOpen && n == len(t) {
				last().Output.Text = strings.Join(outBody, "\n")
				last().spanEnd = i + 1
				outOpen = -1
				continue
			}
			outBody = append(outBody, raw)
		case other != "":
			// Inside some other fenced block (a ```sh example, say): a mote
			// fence there is text, not a cell.
			if strings.HasPrefix(t, other) && strings.Trim(t, other[:1]) == "" {
				other = ""
			}
		case fresh && t == "":
			// Blank lines may separate a cell from its output.
		case fresh && isOutputFence(t):
			fresh = false
			key, err := outputKey(t)
			if err != nil {
				return nil, fmt.Errorf("line %d: %v", ln, err)
			}
			last().Output = &Output{Key: key}
			outOpen, outBody, outLine = fenceLen(t), nil, ln
		case strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~"):
			fresh = false
			marker := t[:3]
			info := strings.Fields(strings.TrimLeft(t, marker[:1]))
			if len(info) == 0 || info[0] != "mote" || marker != "```" {
				other = marker
				continue
			}
			c := Cell{Line: ln}
			for _, attr := range info[1:] {
				k, v, ok := strings.Cut(attr, "=")
				if !ok || k != "as" {
					return nil, fmt.Errorf("line %d: unknown cell attribute %q; the only one is as=NAME", ln, attr)
				}
				if !nameRe.MatchString(v) {
					return nil, fmt.Errorf("line %d: %q is not a name; use letters, digits and _", ln, v)
				}
				if first, dup := bound[v]; dup {
					return nil, fmt.Errorf("line %d: %q is already bound at line %d", ln, v, first)
				}
				c.Name = v
			}
			cur, body, inCell = c, nil, true
		default:
			fresh = false
		}
	}
	if inCell {
		return nil, fmt.Errorf("line %d: cell is never closed", cur.Line)
	}
	if outOpen >= 0 {
		return nil, fmt.Errorf("line %d: output is never closed", outLine)
	}
	return &b, nil
}

// fenceLen is the length of the run of backticks a line starts with.
func fenceLen(t string) int {
	return len(t) - len(strings.TrimLeft(t, "`"))
}

func isOutputFence(t string) bool {
	if fenceLen(t) < 3 {
		return false
	}
	info := strings.Fields(t[fenceLen(t):])
	return len(info) > 0 && info[0] == "output"
}

// outputKey reads the key= attribute of an output fence.
func outputKey(t string) (string, error) {
	var key string
	for _, attr := range strings.Fields(t[fenceLen(t):])[1:] {
		k, v, ok := strings.Cut(attr, "=")
		if !ok || k != "key" {
			return "", fmt.Errorf("unknown output attribute %q; the only one is key=HASH", attr)
		}
		key = v
	}
	return key, nil
}

// Append adds a cell at the end and returns its index. It refuses what Parse
// would: an empty cell, a name that is malformed or already bound, a
// reference to a name nothing binds, and, since it could not be told from
// the end of the cell, a line of only ```.
func (b *Book) Append(name, expr string) (int, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return 0, fmt.Errorf("empty cell")
	}
	if name != "" && !nameRe.MatchString(name) {
		return 0, fmt.Errorf("%q is not a name; use letters, digits and _", name)
	}
	bound := map[string]int{}
	for _, c := range b.Cells {
		if c.Name != "" {
			bound[c.Name] = c.Line
		}
	}
	if line, dup := bound[name]; name != "" && dup {
		return 0, fmt.Errorf("%q is already bound at line %d", name, line)
	}
	body := strings.Split(expr, "\n")
	for _, l := range body {
		if strings.TrimSpace(l) == "```" {
			return 0, fmt.Errorf("a line of ``` would end the cell")
		}
	}
	for _, r := range Refs(expr) {
		if _, ok := bound[r]; !ok {
			return 0, fmt.Errorf("{{%s}} is not bound by an earlier cell", r)
		}
	}

	// The lines end with an empty one when the text ended with a newline;
	// the new cell goes before it, set apart from what precedes by a blank.
	lines := b.lines
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	open := len(lines)
	fence := "```mote"
	if name != "" {
		fence += " as=" + name
	}
	lines = append(append(append(lines, fence), body...), "```", "")
	closeLine := len(lines) - 2
	b.lines = lines
	b.Cells = append(b.Cells, Cell{
		Index: len(b.Cells), Line: open + 1, Name: name, Expr: expr, Refs: Refs(expr),
		closeLine: closeLine, spanEnd: closeLine + 1,
	})
	return len(b.Cells) - 1, nil
}

// AppendProse adds a paragraph of Markdown at the end, set apart from what
// precedes it. It refuses text with a line that opens a fenced block, since
// that could turn into a cell, or swallow the cells after it.
func (b *Book) AppendProse(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("empty text")
	}
	body := strings.Split(text, "\n")
	for _, l := range body {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			return fmt.Errorf("text cannot hold a fenced block")
		}
	}
	lines := b.lines
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	b.lines = append(append(lines, body...), "")
	return nil
}

// Set keeps o under the cell at index i, replacing what was there.
func (b *Book) Set(i int, o Output) { b.Cells[i].Output = &o }

// String renders the notebook: everything as it was read, with each cell's
// output beneath it after one blank line. Rendering a book that was just
// parsed changes nothing but the blank lines between a cell and its output,
// and line ends are always \n.
func (b *Book) String() string {
	var out []string
	pos := 0
	for _, c := range b.Cells {
		out = append(out, b.lines[pos:c.closeLine+1]...)
		if c.Output != nil {
			out = append(out, "")
			out = append(out, c.Output.render()...)
		}
		pos = c.spanEnd
	}
	out = append(out, b.lines[pos:]...)
	return strings.Join(out, "\n")
}

// render writes an output as a fence longer than any run of backticks in
// its text, so the text can never close it early.
func (o Output) render() []string {
	n := 3
	for _, line := range strings.Split(o.Text, "\n") {
		if l := fenceLen(strings.TrimSpace(line)); l >= n {
			n = l + 1
		}
	}
	fence := strings.Repeat("`", n)
	open := fence + "output"
	if o.Key != "" {
		open += " key=" + o.Key
	}
	return []string{open, o.Text, fence}
}

// Refs lists the distinct names {{name}} refers to in s, in order of first use.
func Refs(expr string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range refRe.FindAllStringSubmatch(expr, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// Substitute replaces each {{name}} in s with its value. A name lookup
// cannot find is left as written, so the caller can report it.
func Substitute(s string, lookup func(name string) (string, bool)) string {
	return refRe.ReplaceAllStringFunc(s, func(m string) string {
		name := refRe.FindStringSubmatch(m)[1]
		if v, ok := lookup(name); ok {
			return v
		}
		return m
	})
}

// Key fingerprints a cell for the output cache: its text and the values it
// reads, so editing a cell or changing anything upstream of it changes the
// key, and nothing else does. inputs maps each referenced name to its value.
func Key(c Cell, inputs map[string]string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%q\n", c.Expr)
	names := make([]string, 0, len(c.Refs))
	names = append(names, c.Refs...)
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(h, "%s=%q\n", n, inputs[n])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// FromSegments builds a notebook from prose and cells, the way a page that
// edits one hands it back. It fails, naming the problem, when the result would
// not be a valid notebook: a name read before it is bound, bound twice, or a
// line that would turn prose into a cell or end a cell early.
func FromSegments(segs []Segment) (*Book, error) {
	var parts []string
	var want []Cell
	for _, seg := range segs {
		if seg.Cell == nil {
			if text := strings.TrimSpace(seg.Prose); text != "" {
				parts = append(parts, text)
			}
			continue
		}
		c := seg.Cell
		expr := strings.TrimSpace(c.Expr)
		if expr == "" {
			return nil, fmt.Errorf("empty cell")
		}
		if c.Name != "" && !nameRe.MatchString(c.Name) {
			return nil, fmt.Errorf("%q is not a name; use letters, digits and _", c.Name)
		}
		fence := "```mote"
		if c.Name != "" {
			fence += " as=" + c.Name
		}
		part := fence + "\n" + expr + "\n```"
		if c.Output != nil {
			part += "\n\n" + strings.Join(c.Output.render(), "\n")
		}
		parts = append(parts, part)
		want = append(want, Cell{Name: c.Name, Expr: expr})
	}
	book, err := Parse(strings.Join(parts, "\n\n") + "\n")
	if err != nil {
		return nil, err
	}
	if err := checkCells(book, want); err != nil {
		return nil, err
	}
	return book, nil
}
