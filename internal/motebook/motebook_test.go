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
