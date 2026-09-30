package motebook

import (
	"strings"
	"testing"
)

const sample = "# Notes\n\nSome prose.\n\n" +
	"```mote as=transcript\ntranscribe meeting.m4a\n```\n\n" +
	"More prose.\n\n" +
	"```mote as=summary\nchat \"Summarise: {{transcript}}\"\n```\n\n" +
	"```mote\nchat 'again {{ summary }} and {{transcript}} {{summary}}'\n```\n"

func TestParse(t *testing.T) {
	b, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Cells) != 3 {
		t.Fatalf("cells = %d, want 3", len(b.Cells))
	}
	c := b.Cells[1]
	if c.Name != "summary" || c.Index != 1 || c.Line != 11 || len(c.Refs) != 1 || c.Refs[0] != "transcript" {
		t.Errorf("cell 1 = %+v", c)
	}
	if got := b.Cells[2].Refs; strings.Join(got, ",") != "summary,transcript" {
		t.Errorf("refs = %v, want summary,transcript in first-use order", got)
	}
	if b.Cells[2].Name != "" {
		t.Errorf("unnamed cell has name %q", b.Cells[2].Name)
	}
}

func TestParseIgnoresOtherFences(t *testing.T) {
	src := "```sh\n```mote\nnot a cell\n```\n\n~~~\n```mote as=x\nnope\n~~~\n\n```text\nx\n```\n" +
		"```motebook\nalso not\n```\n```mote\nchat hi\n```\n"
	b, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Cells) != 1 || b.Cells[0].Expr != "chat hi" {
		t.Fatalf("cells = %+v, want only the last", b.Cells)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"```mote\n\n```":                                         "line 1: empty cell",
		"```mote\nchat hi":                                       "line 1: cell is never closed",
		"```mote as=a b\nx\n```":                                 "unknown cell attribute",
		"```mote as=a-b\nx\n```":                                 "not a name",
		"```mote color=red\nx\n```":                              "unknown cell attribute",
		"```mote as=a\nx\n```\n```mote as=a\ny\n```":             `line 4: "a" is already bound at line 1`,
		"```mote\nchat {{nope}}\n```":                            "{{nope}} is not bound",
		"```mote as=a\nchat {{a}}\n```":                          "{{a}} is not bound",
		"```mote\nchat {{later}}\n```\n```mote as=later\nx\n```": "{{later}} is not bound",
	}
	for src, want := range cases {
		_, err := Parse(src)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) error = %v, want %q", src, err, want)
		}
	}
}

func TestParseCRLF(t *testing.T) {
	b, err := Parse(strings.ReplaceAll(sample, "\n", "\r\n"))
	if err != nil || len(b.Cells) != 3 {
		t.Fatalf("cells = %v, err = %v", b, err)
	}
}

func TestSubstituteLeavesUnknownAndKeepsValuesLiteral(t *testing.T) {
	vals := map[string]string{"a": `x | "y" {{b}}`}
	got := Substitute("{{a}} {{ a }} {{b}}", func(n string) (string, bool) { v, ok := vals[n]; return v, ok })
	want := `x | "y" {{b}} x | "y" {{b}} {{b}}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestKey(t *testing.T) {
	c := Cell{Expr: "chat {{a}} {{b}}", Refs: []string{"a", "b"}}
	base := Key(c, map[string]string{"a": "1", "b": "2"})
	if Key(c, map[string]string{"b": "2", "a": "1"}) != base {
		t.Error("key depends on map order")
	}
	if Key(c, map[string]string{"a": "1", "b": "3"}) == base {
		t.Error("key ignores an input")
	}
	if Key(c, map[string]string{"a": "1", "b": "2", "unrelated": "z"}) != base {
		t.Error("key depends on a value the cell does not read")
	}
	c2 := c
	c2.Expr = "chat {{b}} {{a}}"
	if Key(c2, map[string]string{"a": "1", "b": "2"}) == base {
		t.Error("key ignores the cell text")
	}
}

const ran = "# Notes\n\n" +
	"```mote as=a\nchat hi\n```\n\n```output key=abc123\nhello\nthere\n```\n\nprose\n\n" +
	"```mote\nchat {{a}}\n```\n\nno output yet\n"

func TestParseReadsOutputs(t *testing.T) {
	b, err := Parse(ran)
	if err != nil {
		t.Fatal(err)
	}
	if o := b.Cells[0].Output; o == nil || o.Key != "abc123" || o.Text != "hello\nthere" {
		t.Errorf("output 0 = %+v", o)
	}
	if b.Cells[1].Output != nil {
		t.Errorf("output 1 = %+v, want none", b.Cells[1].Output)
	}
}

func TestOutputFenceOnlyBelongsToTheCellRightAbove(t *testing.T) {
	src := "```mote\nchat hi\n```\n\nprose\n\n```output\nstray\n```\n"
	b, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if b.Cells[0].Output != nil {
		t.Errorf("a stray output fence was taken for the cell's: %+v", b.Cells[0].Output)
	}
	if b.String() != src {
		t.Errorf("stray fence not left alone:\n%s", b.String())
	}
}

func TestRenderIsStable(t *testing.T) {
	b, err := Parse(ran)
	if err != nil {
		t.Fatal(err)
	}
	once := b.String()
	if once != ran {
		t.Errorf("rendering a parsed book changed it:\n%s", once)
	}
	// Blank lines between a cell and its output are the one thing normalised.
	tight := strings.Replace(ran, "```\n\n```output", "```\n```output", 1)
	b2, err := Parse(tight)
	if err != nil {
		t.Fatal(err)
	}
	if b2.String() != ran {
		t.Errorf("output not set apart from its cell:\n%s", b2.String())
	}
}

func TestSetReplacesAndInserts(t *testing.T) {
	b, err := Parse(ran)
	if err != nil {
		t.Fatal(err)
	}
	b.Set(0, Output{Key: "new", Text: "changed"})
	b.Set(1, Output{Text: "fresh"})
	got := b.String()
	want := strings.Replace(ran, "```output key=abc123\nhello\nthere\n```", "```output key=new\nchanged\n```", 1)
	want = strings.Replace(want, "```\n\nno output yet", "```\n\n```output\nfresh\n```\n\nno output yet", 1)
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	again, err := Parse(got)
	if err != nil || again.String() != got {
		t.Errorf("rendered book does not read back the same: %v", err)
	}
	if o := again.Cells[1].Output; o == nil || o.Key != "" || o.Text != "fresh" {
		t.Errorf("read back %+v", o)
	}
}

func TestOutputWithBackticksGetsALongerFence(t *testing.T) {
	b, err := Parse("```mote\nchat hi\n```\n")
	if err != nil {
		t.Fatal(err)
	}
	text := "here:\n```go\nx := 1\n```\n````\nfour\n````"
	b.Set(0, Output{Key: "k", Text: text})
	again, err := Parse(b.String())
	if err != nil || len(again.Cells) != 1 || again.Cells[0].Output == nil || again.Cells[0].Output.Text != text {
		t.Fatalf("round trip failed: %v\n%s", err, b.String())
	}
	if !strings.HasPrefix(b.String(), "```mote\nchat hi\n```\n\n`````output key=k\n") {
		t.Errorf("fence not lengthened:\n%s", b.String())
	}
}

func TestEmptyOutputRoundTrips(t *testing.T) {
	b, _ := Parse("```mote\nchat hi\n```\n")
	b.Set(0, Output{Key: "k"})
	again, err := Parse(b.String())
	if err != nil || again.Cells[0].Output == nil || again.Cells[0].Output.Text != "" {
		t.Fatalf("round trip failed: %v\n%s", err, b.String())
	}
}

func TestOutputErrors(t *testing.T) {
	cases := map[string]string{
		"```mote\nx\n```\n```output\nnever closed":    "line 4: output is never closed",
		"```mote\nx\n```\n```output color=red\n\n```": "line 4: unknown output attribute",
	}
	for src, want := range cases {
		if _, err := Parse(src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) error = %v, want %q", src, err, want)
		}
	}
}

func TestAppendAddsCellsThatReadBackTheSame(t *testing.T) {
	for name, start := range map[string]string{
		"empty":                "",
		"prose":                "# Notes\n",
		"no trailing newline":  "# Notes",
		"a cell with output":   "```mote as=a\nchat hi\n```\n\n```output key=k\nhello\n```\n",
		"a cell without":       "```mote as=a\nchat hi\n```\n",
		"prose after the cell": "```mote as=a\nchat hi\n```\n\ntrailing prose\n",
	} {
		b, err := Parse(start)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		before := len(b.Cells)
		i, err := b.Append("z", "chat \"multi\nline\"")
		if err != nil || i != before {
			t.Fatalf("%s: Append = %d, %v", name, i, err)
		}
		b.Set(i, Output{Key: "kk", Text: "out"})
		text := b.String()
		if !strings.HasSuffix(text, "```\n\n```output key=kk\nout\n```\n") {
			t.Errorf("%s: cell not last:\n%q", name, text)
		}
		if strings.Contains(text, "\n\n\n") {
			t.Errorf("%s: doubled blank line:\n%q", name, text)
		}
		again, err := Parse(text)
		if err != nil || len(again.Cells) != before+1 || again.String() != text {
			t.Fatalf("%s: does not read back the same: %v\n%s", name, err, text)
		}
		got := again.Cells[i]
		if got.Name != "z" || got.Expr != "chat \"multi\nline\"" || got.Output == nil || got.Output.Text != "out" || got.Line != b.Cells[i].Line {
			t.Errorf("%s: cell %+v (line %d in the original)", name, got, b.Cells[i].Line)
		}
		// Old cells and their outputs are undisturbed.
		for k := 0; k < before; k++ {
			if again.Cells[k].Expr != b.Cells[k].Expr {
				t.Errorf("%s: cell %d changed", name, k)
			}
		}
	}
}

func TestAppendRefuses(t *testing.T) {
	b, _ := Parse("```mote as=a\nchat hi\n```\n")
	cases := []struct{ name, expr, want string }{
		{"", "  \n", "empty cell"},
		{"a-b", "chat x", "not a name"},
		{"a", "chat x", `"a" is already bound at line 1`},
		{"", "chat {{nope}}", "{{nope}} is not bound"},
		{"", "chat \"x\n```\ny\"", "would end the cell"},
	}
	for _, c := range cases {
		before := b.String()
		if _, err := b.Append(c.name, c.expr); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Append(%q, %q) = %v, want %q", c.name, c.expr, err, c.want)
		}
		if b.String() != before || len(b.Cells) != 1 {
			t.Errorf("a refused Append changed the book")
		}
	}
	// A name earlier cells bind may be read.
	if _, err := b.Append("", "chat {{a}}"); err != nil {
		t.Errorf("reading a bound name: %v", err)
	}
}

func TestSplitBinding(t *testing.T) {
	cases := []struct{ in, name, expr string }{
		{`chat hello`, "", `chat hello`},
		{`city = chat "capital of France"`, "city", `chat "capital of France"`},
		{`city=chat hi`, "city", `chat hi`},
		{"a = \nchat x", "a", "chat x"}, // a backslash continuation after the =
		{"a =", "", "a ="},
		{`chat "a=b"`, "", `chat "a=b"`},
		{`sh: x=1`, "", `sh: x=1`},
		{"a = chat x |\n chat y", "a", "chat x |\n chat y"},
	}
	for _, c := range cases {
		if name, expr := SplitBinding(c.in); name != c.name || expr != c.expr {
			t.Errorf("SplitBinding(%q) = %q, %q; want %q, %q", c.in, name, expr, c.name, c.expr)
		}
	}
}

func TestAppendProseAddsTextThatReadsBackTheSame(t *testing.T) {
	for name, start := range map[string]string{
		"empty":               "",
		"prose":               "# Notes\n",
		"no trailing newline": "# Notes",
		"after a cell":        "```mote as=a\nchat hi\n```\n",
		"after an output":     "```mote as=a\nchat hi\n```\n\n```output key=k\nhello\n```\n",
		"before prose":        "```mote\nchat hi\n```\n\ntrailing prose\n",
	} {
		b, err := Parse(start)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := b.AppendProse("  A note\nover two lines.  "); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := b.Append("z", "chat again"); err != nil { // a cell after the text keeps working
			t.Fatalf("%s: %v", name, err)
		}
		text := b.String()
		if strings.Contains(text, "\n\n\n") {
			t.Errorf("%s: doubled blank line:\n%q", name, text)
		}
		again, err := Parse(text)
		if err != nil || again.String() != text || len(again.Cells) != len(b.Cells) {
			t.Fatalf("%s: does not read back the same: %v\n%s", name, err, text)
		}
		var found bool
		for _, seg := range again.Segments() {
			found = found || strings.Contains(seg.Prose, "A note\nover two lines.")
		}
		if !found {
			t.Errorf("%s: the note is not prose after reading back:\n%s", name, text)
		}
	}
}

func TestAppendProseRefuses(t *testing.T) {
	b, _ := Parse("```mote\nchat hi\n```\n")
	before := b.String()
	for text, want := range map[string]string{
		"  \n ":                   "empty text",
		"a\n```mote\nchat x\n```": "fenced block",
		"~~~\nx":                  "fenced block",
		"   ```":                  "fenced block",
	} {
		if err := b.AppendProse(text); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("AppendProse(%q) = %v, want %q", text, err, want)
		}
		if b.String() != before {
			t.Errorf("a refused AppendProse changed the book")
		}
	}
}

func TestKind(t *testing.T) {
	for path, want := range map[string]string{
		"a.png": "image", "dir/B.JPG": "image", "x.svg": "image",
		"m.m4a": "audio", "n.MP3": "audio",
		"clip.mp4": "video", "v.webm": "video",
		"notes.txt": "", "noext": "", "": "", "archive.png.zip": "",
	} {
		if got := Kind(path); got != want {
			t.Errorf("Kind(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestFromSegmentsRebuildsTheNotebookItCameFrom(t *testing.T) {
	b, err := Parse(converted)
	if err != nil {
		t.Fatal(err)
	}
	again, err := FromSegments(b.Segments())
	if err != nil {
		t.Fatal(err)
	}
	if again.String() != b.String() {
		t.Errorf("segments do not give the notebook back:\n%s\nwant:\n%s", again.String(), b.String())
	}
	if again.Cells[0].Output == nil || again.Cells[0].Output.Key != "abc123" {
		t.Errorf("an output was lost: %+v", again.Cells[0].Output)
	}
	if empty, err := FromSegments(nil); err != nil || len(empty.Cells) != 0 || len(empty.Segments()) != 0 {
		t.Errorf("no segments: %v", err)
	}
}

func TestFromSegmentsEdits(t *testing.T) {
	b, _ := Parse("```mote as=a\nchat hi\n```\n\n```mote\nchat \"{{a}}\"\n```\n")
	segs := b.Segments()

	// Insert, in the middle, a cell and some text.
	added := []Segment{segs[0], {Prose: "Between."}, {Cell: &Cell{Name: "b", Expr: "chat two"}}, segs[1]}
	got, err := FromSegments(added)
	if err != nil {
		t.Fatal(err)
	}
	want := "```mote as=a\nchat hi\n```\n\nBetween.\n\n```mote as=b\nchat two\n```\n\n```mote\nchat \"{{a}}\"\n```\n"
	if got.String() != want {
		t.Errorf("insert:\n%s\nwant:\n%s", got.String(), want)
	}
	// Edit a cell and drop its output, which no longer belongs to it.
	edited := &Cell{Name: "a", Expr: "chat changed"}
	if got, err := FromSegments([]Segment{{Cell: edited}, segs[1]}); err != nil || got.Cells[0].Output != nil || got.Cells[0].Expr != "chat changed" {
		t.Errorf("edit: %v", err)
	}
	// Move a cell after the one that reads it, and delete the one it reads.
	if _, err := FromSegments([]Segment{segs[1], segs[0]}); err == nil || !strings.Contains(err.Error(), "{{a}} is not bound") {
		t.Errorf("reader before binder: %v", err)
	}
	if _, err := FromSegments([]Segment{segs[1]}); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Errorf("deleting what is read: %v", err)
	}

	cases := []struct {
		name string
		segs []Segment
		want string
	}{
		{"the same name twice", []Segment{segs[0], {Cell: &Cell{Name: "a", Expr: "chat x"}}}, "already bound"},
		{"an empty cell", []Segment{{Cell: &Cell{Expr: " \n"}}}, "empty cell"},
		{"a bad name", []Segment{{Cell: &Cell{Name: "a-b", Expr: "chat x"}}}, "not a name"},
		{"a cell that would end early", []Segment{{Cell: &Cell{Expr: "chat \"a\n```\nb\""}}}, "line of ```"},
		{"prose that would be a cell", []Segment{{Prose: "```mote\nchat hi\n```"}}, "```mote fence"},
	}
	for _, c := range cases {
		if _, err := FromSegments(c.segs); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	// Blank text is not a segment worth keeping.
	if got, err := FromSegments([]Segment{{Prose: "  \n"}, segs[0]}); err != nil || strings.HasPrefix(got.String(), "\n") {
		t.Errorf("blank prose: %v", err)
	}
}
