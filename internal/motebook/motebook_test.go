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
