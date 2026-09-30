package motebook

import (
	"encoding/json"
	"strings"
	"testing"
)

const converted = "# Notes\n\nSome prose\nover two lines.\n\n" +
	"```mote as=a\nchat hi\n```\n\n```output key=abc123\nhello\nthere\n```\n\n" +
	"Between.\n\n" +
	"```sh\nnot a cell\n```\n\n" +
	"```mote\nchat \"{{a}}\"\n```\n\nThe end.\n"

func TestSegmentsListProseAndCellsInOrder(t *testing.T) {
	b, err := Parse(converted)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range b.Segments() {
		if s.Cell != nil {
			got = append(got, "cell:"+s.Cell.Expr)
		} else {
			got = append(got, "prose:"+strings.ReplaceAll(s.Prose, "\n", "/"))
		}
	}
	want := []string{
		"prose:# Notes//Some prose/over two lines.",
		"cell:chat hi",
		"prose:Between.//```sh/not a cell/```",
		`cell:chat "{{a}}"`,
		"prose:The end.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("segments:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if empty, _ := Parse(""); len(empty.Segments()) != 0 {
		t.Errorf("an empty notebook has segments: %v", empty.Segments())
	}
}

func TestIPYNBIsANotebookForTheMoteKernel(t *testing.T) {
	b, _ := Parse(converted)
	data, err := b.IPYNB()
	if err != nil {
		t.Fatal(err)
	}
	var nb struct {
		NBFormat      int `json:"nbformat"`
		NBFormatMinor int `json:"nbformat_minor"`
		Metadata      struct {
			Kernelspec struct{ Name, Language string } `json:"kernelspec"`
		} `json:"metadata"`
		Cells []struct {
			CellType       string   `json:"cell_type"`
			Source         []string `json:"source"`
			ExecutionCount *int     `json:"execution_count"`
			Outputs        []struct {
				OutputType string   `json:"output_type"`
				Name       string   `json:"name"`
				Text       []string `json:"text"`
			} `json:"outputs"`
		} `json:"cells"`
	}
	if err := json.Unmarshal(data, &nb); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if nb.NBFormat != 4 || nb.Metadata.Kernelspec.Name != "mote" || nb.Metadata.Kernelspec.Language != "mote" {
		t.Errorf("header %+v", nb)
	}
	types := ""
	for _, c := range nb.Cells {
		types += c.CellType[:1]
	}
	if types != "mcmcm" {
		t.Fatalf("cell types %q", types)
	}
	first := nb.Cells[1]
	if strings.Join(first.Source, "") != "a = chat hi" {
		t.Errorf("a named cell reads %q, as the kernel takes it", first.Source)
	}
	if first.ExecutionCount == nil || *first.ExecutionCount != 1 || len(first.Outputs) != 1 ||
		first.Outputs[0].OutputType != "stream" || first.Outputs[0].Name != "stdout" ||
		strings.Join(first.Outputs[0].Text, "") != "hello\nthere\n" {
		t.Errorf("output %+v", first)
	}
	if got := nb.Cells[0].Source; len(got) != 4 || got[0] != "# Notes\n" || got[3] != "over two lines." {
		t.Errorf("markdown source %q", got)
	}
	// A cell that has not run still carries the fields Jupyter requires.
	if !strings.Contains(string(data), `"execution_count": null`) || !strings.Contains(string(data), `"outputs": []`) {
		t.Errorf("an unrun code cell lacks execution_count or outputs:\n%s", data)
	}
	if strings.Contains(string(data), "abc123") {
		t.Error("the fingerprint has no place in a notebook")
	}
	if !strings.HasSuffix(string(data), "}\n") {
		t.Error("no trailing newline")
	}
}

func TestNotebookSurvivesTheRoundTrip(t *testing.T) {
	b, _ := Parse(converted)
	data, err := b.IPYNB()
	if err != nil {
		t.Fatal(err)
	}
	back, err := FromIPYNB(data, false)
	if err != nil {
		t.Fatal(err)
	}
	// Everything but the fingerprint, which nothing outside mote can vouch for.
	want := strings.Replace(converted, "```output key=abc123", "```output", 1)
	if back.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", back.String(), want)
	}
	if back.Cells[0].Output.Key != "" {
		t.Error("an imported output would be trusted")
	}
	if back.Cells[0].Name != "a" || back.Cells[1].Refs[0] != "a" {
		t.Errorf("names lost: %+v", back.Cells)
	}
}

func TestOutputWithBackticksSurvivesTheRoundTrip(t *testing.T) {
	b, _ := Parse("```mote\nchat hi\n```\n")
	text := "here:\n```go\nx := 1\n```"
	b.Set(0, Output{Text: text})
	data, _ := b.IPYNB()
	back, err := FromIPYNB(data, false)
	if err != nil || back.Cells[0].Output == nil || back.Cells[0].Output.Text != text {
		t.Fatalf("round trip: %v\n%s", err, back)
	}
}

func TestFromIPYNBReadsWhatJupyterWrites(t *testing.T) {
	// Text as one string or as lines, raw and empty cells, outputs other than
	// stdout, and code that prints nothing.
	src := `{
 "nbformat": 4, "nbformat_minor": 5,
 "metadata": {"kernelspec": {"name": "mote", "display_name": "mote"}},
 "cells": [
  {"cell_type": "markdown", "metadata": {}, "source": "# Title"},
  {"cell_type": "raw", "metadata": {}, "source": ["ignored"]},
  {"cell_type": "code", "metadata": {}, "execution_count": 3, "source": "city = chat capital", "outputs": [
     {"output_type": "stream", "name": "stderr", "text": "warning\n"},
     {"output_type": "stream", "name": "stdout", "text": ["Paris\n"]},
     {"output_type": "display_data", "data": {"text/plain": "x"}, "metadata": {}}
  ]},
  {"cell_type": "code", "metadata": {}, "source": ["  \n"], "outputs": [], "execution_count": null},
  {"cell_type": "code", "metadata": {}, "source": ["chat ", "\"{{city}}\""], "outputs": [], "execution_count": null}
 ]}`
	b, err := FromIPYNB([]byte(src), false)
	if err != nil {
		t.Fatal(err)
	}
	// What the mote kernel shows as an image or a player comes back as the path
	// it stands for: its text/plain.
	want := "# Title\n\n```mote as=city\nchat capital\n```\n\n```output\nParis\nx\n```\n\n```mote\nchat \"{{city}}\"\n```\n"
	if b.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestFromIPYNBRefuses(t *testing.T) {
	code := func(src string) string {
		return `{"cell_type": "code", "metadata": {}, "source": ` + src + `, "outputs": [], "execution_count": null}`
	}
	book := func(kernel string, minor int, cells ...string) string {
		return `{"nbformat": ` + strings.Repeat("4", minor) + `, "metadata": {"kernelspec": {"name": "` + kernel + `"}}, "cells": [` + strings.Join(cells, ",") + `]}`
	}
	cases := []struct {
		name, src, want string
	}{
		{"not JSON", "{nope", "not a Jupyter notebook"},
		{"an old format", `{"nbformat": 3, "cells": []}`, "nbformat 3"},
		{"another kernel", book("python3", 1, code(`"print(1)"`)), "kernel is python3"},
		{"no kernel", `{"nbformat": 4, "metadata": {}, "cells": []}`, "kernel is none"},
		{"an unknown cell type", book("mote", 1, `{"cell_type": "widget", "source": ""}`), `type "widget"`},
		{"text that is not text", book("mote", 1, `{"cell_type": "markdown", "metadata": {}, "source": 7}`), "not a Jupyter notebook"},
		{"a name nothing binds", book("mote", 1, code(`"chat {{x}}"`)), "not bound"},
		{"a cell that would end early", book("mote", 1, code(`"chat \"a\n`+"```"+`\nb\""`)), "holds a line of ```"},
		{"prose that holds a cell", book("mote", 1, `{"cell_type": "markdown", "metadata": {}, "source": "`+"```mote\\nchat hi\\n```"+`"}`), "holds a ```mote fence"},
	}
	for _, c := range cases {
		if _, err := FromIPYNB([]byte(c.src), false); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	// Another kernel's notebook is read when asked for.
	other := book("python3", 1, code(`"chat hi"`))
	if b, err := FromIPYNB([]byte(other), true); err != nil || len(b.Cells) != 1 {
		t.Errorf("anyKernel: %v", err)
	}
}

func TestSplitLines(t *testing.T) {
	for in, want := range map[string]string{
		"":         "",
		"a":        "a",
		"a\n":      "a\n",
		"a\nb":     "a\n|b",
		"a\n\nb\n": "a\n|\n|b\n",
	} {
		if got := strings.Join(splitLines(in), "|"); got != want {
			t.Errorf("splitLines(%q) = %q, want %q", in, got, want)
		}
		if strings.Join(splitLines(in), "") != in {
			t.Errorf("splitLines(%q) loses text", in)
		}
	}
	if b, _ := (lines)(nil).MarshalJSON(); string(b) != "[]" {
		t.Errorf("nil lines marshal to %s, not an empty list", b)
	}
}

func TestIPYNBOfAnEmptyNotebookHasAListOfCells(t *testing.T) {
	b, _ := Parse("")
	data, err := b.IPYNB()
	if err != nil || !strings.Contains(string(data), `"cells": []`) {
		t.Errorf("%v\n%s", err, data)
	}
}

func TestIPYNBRoundTripsImagesAndEmptyOutputs(t *testing.T) {
	src := `{"nbformat": 4, "metadata": {"kernelspec": {"name": "mote"}}, "cells": [
	  {"cell_type": "code", "metadata": {}, "execution_count": 1, "source": "frames clip.mp4 2", "outputs": [
	    {"output_type": "display_data", "metadata": {}, "data": {"image/png": "iVBOR", "text/plain": "frames/frame_001.png"}},
	    {"output_type": "display_data", "metadata": {}, "data": {"image/png": "iVBOR", "text/plain": ["frames/frame_002.png"]}}
	  ]},
	  {"cell_type": "code", "metadata": {}, "execution_count": 2, "source": "sh: true", "outputs": []}
	]}`
	b, err := FromIPYNB([]byte(src), false)
	if err != nil {
		t.Fatal(err)
	}
	if o := b.Cells[0].Output; o == nil || o.Text != "frames/frame_001.png\nframes/frame_002.png" {
		t.Errorf("images: %+v", o)
	}
	if o := b.Cells[1].Output; o == nil || o.Text != "" {
		t.Errorf("a cell that ran and printed nothing: %+v", o)
	}
	// And out again: it still ran.
	data, _ := b.IPYNB()
	back, err := FromIPYNB(data, false)
	if err != nil || back.Cells[1].Output == nil {
		t.Errorf("round trip: %v %+v", err, back)
	}
}

func TestIPYNBRefusesACellAJupyterCellWouldReadAsABinding(t *testing.T) {
	b, _ := Parse("```mote\nsay = hi\n```\n")
	if _, err := b.IPYNB(); err == nil || !strings.Contains(err.Error(), `binding of "say"`) {
		t.Errorf("%v", err)
	}
	named, _ := Parse("```mote as=x\nsay = hi\n```\n")
	if _, err := named.IPYNB(); err != nil {
		t.Errorf("a named cell: %v", err)
	}
}
