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
// The package only parses and fingerprints; running a cell is the CLI's job,
// because a value must be substituted into parsed arguments, never into the
// pipeline text, or a value containing a | or a quote would change its shape.
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
}

// Book is a parsed notebook.
type Book struct {
	Cells []Cell
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
		b      Book
		bound  = map[string]int{} // name -> line of the cell that binds it
		lines  = strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
		inCell bool
		other  string // fence marker of a non-mote block we are inside
		cur    Cell
		body   []string
	)
	for i, raw := range lines {
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
				cur.Refs = refs(cur.Expr)
				for _, r := range cur.Refs {
					if _, ok := bound[r]; !ok {
						return nil, fmt.Errorf("line %d: {{%s}} is not bound by an earlier cell", cur.Line, r)
					}
				}
				if cur.Name != "" {
					bound[cur.Name] = cur.Line
				}
				cur.Index = len(b.Cells)
				b.Cells = append(b.Cells, cur)
				continue
			}
			body = append(body, raw)
		case other != "":
			// Inside some other fenced block (a ```sh example, say): a mote
			// fence there is text, not a cell.
			if strings.HasPrefix(t, other) && strings.Trim(t, other[:1]) == "" {
				other = ""
			}
		case strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~"):
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
		}
	}
	if inCell {
		return nil, fmt.Errorf("line %d: cell is never closed", cur.Line)
	}
	return &b, nil
}

// refs lists the distinct names a cell uses, in order of first use.
func refs(expr string) []string {
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
