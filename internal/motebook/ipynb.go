package motebook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// A Jupyter notebook and a motebook hold the same things: prose, cells in
// order and what each produced. IPYNB writes a motebook as an nbformat 4
// notebook for the mote kernel, and FromIPYNB reads one back, so a notebook
// can be opened in Jupyter and run with `mote nb run`, or the other way
// round.
//
// In a notebook a cell is written as the kernel takes it, "name = pipeline"
// where a motebook has as=name.

// Segment is one run of a notebook: prose, or a cell.
type Segment struct {
	Prose string // the Markdown between cells, without the blank lines around it
	Cell  *Cell  // set instead of Prose for a cell
}

// Segments lists the notebook's prose and cells in the order they appear.
// Output fences belong to their cell and are not prose.
func (b *Book) Segments() []Segment {
	var segs []Segment
	prose := func(lines []string) {
		start, end := 0, len(lines)
		for start < end && strings.TrimSpace(lines[start]) == "" {
			start++
		}
		for end > start && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		if start < end {
			segs = append(segs, Segment{Prose: strings.Join(lines[start:end], "\n")})
		}
	}
	pos := 0
	for i := range b.Cells {
		c := &b.Cells[i]
		prose(b.lines[pos : c.Line-1])
		segs = append(segs, Segment{Cell: c})
		pos = c.spanEnd
	}
	prose(b.lines[pos:])
	return segs
}

// lines is the JSON form of text in a notebook: an array of lines that keep
// their newlines, or a single string, either of which nbformat allows.
type lines []string

func (l lines) MarshalJSON() ([]byte, error) {
	if l == nil {
		l = lines{}
	}
	return json.Marshal([]string(l))
}

func (l *lines) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*l = lines{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return fmt.Errorf("text is neither a string nor a list of lines")
	}
	*l = many
	return nil
}

func (l lines) String() string { return strings.Join(l, "") }

func splitLines(s string) lines {
	if s == "" {
		return lines{}
	}
	parts := strings.SplitAfter(s, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

type ipynbOutput struct {
	OutputType string           `json:"output_type"`
	Name       string           `json:"name,omitempty"`
	Text       lines            `json:"text,omitempty"`
	Data       map[string]lines `json:"data,omitempty"`
}

type ipynbCell struct {
	CellType       string         `json:"cell_type"`
	Metadata       map[string]any `json:"metadata"`
	Source         lines          `json:"source"`
	ExecutionCount *int           `json:"execution_count,omitempty"`
	Outputs        []ipynbOutput  `json:"outputs,omitempty"`
}

// code cells must carry execution_count and outputs even when empty, which
// omitempty would drop.
func (c ipynbCell) MarshalJSON() ([]byte, error) {
	type plain ipynbCell
	if c.CellType != "code" {
		return json.Marshal(plain(c))
	}
	outputs := c.Outputs
	if outputs == nil {
		outputs = []ipynbOutput{}
	}
	return json.Marshal(struct {
		CellType       string         `json:"cell_type"`
		Metadata       map[string]any `json:"metadata"`
		Source         lines          `json:"source"`
		ExecutionCount *int           `json:"execution_count"`
		Outputs        []ipynbOutput  `json:"outputs"`
	}{c.CellType, c.Metadata, c.Source, c.ExecutionCount, outputs})
}

type ipynb struct {
	Cells         []ipynbCell    `json:"cells"`
	Metadata      map[string]any `json:"metadata"`
	NBFormat      int            `json:"nbformat"`
	NBFormatMinor int            `json:"nbformat_minor"`
}

const kernelLanguage = "mote"

// IPYNB writes the notebook as nbformat 4 for the mote kernel. A cell's
// output is kept as what it printed; the fingerprint that lets `mote nb run`
// skip it has no place in a notebook and is not written.
func (b *Book) IPYNB() ([]byte, error) {
	nb := ipynb{
		NBFormat: 4, NBFormatMinor: 4,
		Metadata: map[string]any{
			"kernelspec": map[string]any{"display_name": "mote", "language": kernelLanguage, "name": "mote"},
			"language_info": map[string]any{
				"name": kernelLanguage, "mimetype": "text/x-sh", "file_extension": ".mote", "codemirror_mode": "shell",
			},
		},
	}
	nb.Cells = []ipynbCell{} // an empty notebook has cells, none of them
	for _, seg := range b.Segments() {
		if seg.Cell == nil {
			nb.Cells = append(nb.Cells, ipynbCell{CellType: "markdown", Metadata: map[string]any{}, Source: splitLines(seg.Prose)})
			continue
		}
		c := seg.Cell
		source := c.Expr
		if c.Name != "" {
			source = c.Name + " = " + c.Expr
		} else if name, _ := SplitBinding(c.Expr); name != "" {
			// A Jupyter cell binds a name by starting with "name =", so this one
			// would come back as a binding of its first word.
			return nil, fmt.Errorf("cell %d (line %d) reads as a binding of %q in a Jupyter cell; give it a name with as=, or change its first argument", c.Index+1, c.Line, name)
		}
		cell := ipynbCell{CellType: "code", Metadata: map[string]any{}, Source: splitLines(source)}
		if c.Output != nil {
			n := c.Index + 1
			cell.ExecutionCount = &n
			if c.Output.Text != "" {
				cell.Outputs = []ipynbOutput{{OutputType: "stream", Name: "stdout", Text: splitLines(c.Output.Text + "\n")}}
			}
		}
		nb.Cells = append(nb.Cells, cell)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", " ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(nb); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// FromIPYNB reads a Jupyter notebook. Its markdown cells become prose and its
// code cells become cells, each "name = pipeline" binding its name; empty
// code cells are dropped. What a cell printed is kept under it, without a
// fingerprint, so `mote nb run` runs it again rather than trust what it
// cannot check. A notebook for another kernel is refused unless
// anyKernel: its code is not written for mote.
func FromIPYNB(data []byte, anyKernel bool) (*Book, error) {
	var nb ipynb
	if err := json.Unmarshal(data, &nb); err != nil {
		return nil, fmt.Errorf("not a Jupyter notebook: %w", err)
	}
	if nb.NBFormat != 4 {
		return nil, fmt.Errorf("nbformat %d is not supported; only 4", nb.NBFormat)
	}
	if spec, _ := nb.Metadata["kernelspec"].(map[string]any); !anyKernel {
		name, _ := spec["name"].(string)
		if name != "mote" {
			if name == "" {
				name = "none"
			}
			return nil, fmt.Errorf("this notebook's kernel is %s, not mote, so its code is not written for mote", name)
		}
	}

	var parts []string
	var want []Cell // the cells the notebook holds, to check what Parse makes of them
	for i, c := range nb.Cells {
		switch c.CellType {
		case "markdown":
			if text := trimBlankLines(c.Source.String()); text != "" {
				parts = append(parts, text)
			}
		case "code":
			source := strings.TrimSpace(c.Source.String())
			if source == "" {
				continue
			}
			name, expr := SplitBinding(source)
			open := "```mote"
			if name != "" {
				open += " as=" + name
			}
			part := open + "\n" + expr + "\n```"
			// What the cell printed, and what the mote kernel shows as an image
			// or a player, whose text is the path mote printed.
			var printed strings.Builder
			for _, o := range c.Outputs {
				switch {
				case o.OutputType == "stream" && o.Name == "stdout":
					printed.WriteString(o.Text.String())
				case o.OutputType == "display_data" || o.OutputType == "execute_result":
					if text, ok := o.Data["text/plain"]; ok {
						printed.WriteString(strings.TrimRight(text.String(), "\n") + "\n")
					}
				}
			}
			var out *Output
			if text := strings.TrimRight(printed.String(), "\n"); text != "" || c.ExecutionCount != nil {
				out = &Output{Text: text} // a cell that ran and printed nothing still ran
				part += "\n\n" + strings.Join(out.render(), "\n")
			}
			parts = append(parts, part)
			want = append(want, Cell{Name: name, Expr: expr, Output: out})
		case "raw":
			// Not for a reader of the notebook, and not a cell.
		default:
			return nil, fmt.Errorf("cell %d has the type %q", i+1, c.CellType)
		}
	}
	book, err := Parse(strings.Join(parts, "\n\n") + "\n")
	if err != nil {
		return nil, fmt.Errorf("the notebook cannot be read as a motebook: %w", err)
	}
	if err := checkCells(book, want); err != nil {
		return nil, fmt.Errorf("the notebook cannot be read as a motebook: %w", err)
	}
	return book, nil
}

// checkCells insists that a notebook built from text has the cells it was
// built from. Parse would read some text differently from how it was written:
// a cell with a line of ``` in it ends early, and prose that holds a mote
// fence turns into a cell of its own.
func checkCells(book *Book, want []Cell) error {
	switch {
	case len(book.Cells) > len(want):
		return fmt.Errorf("a markdown cell holds a ```mote fence")
	case len(book.Cells) < len(want):
		return fmt.Errorf("a cell holds a line of ```, which ends it early and swallows the cells after it")
	}
	for i, c := range book.Cells {
		if c.Name != want[i].Name || c.Expr != want[i].Expr {
			return fmt.Errorf("code cell %d holds a line of ```", i+1)
		}
		if (c.Output == nil) != (want[i].Output == nil) || (c.Output != nil && c.Output.Text != want[i].Output.Text) {
			return fmt.Errorf("the text after cell %d starts with an output fence, which would be read as the cell's output", i+1)
		}
	}
	return nil
}
