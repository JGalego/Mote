package task

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgalego/mote/internal/runtime"
	"github.com/jgalego/mote/registry"
)

// zipDoc writes a zipped office document with the given parts.
func zipDoc(t *testing.T, name string, parts map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for n, body := range parts {
		w, _ := z.Create(n)
		w.Write([]byte(body))
	}
	z.Close()
	f.Close()
	return p
}

func readWith(t *testing.T, e Env, p string, plain bool) (string, error) {
	t.Helper()
	r := &run{ctx: context.Background(), env: e, vars: map[string]Value{}, sessions: map[string]runtime.Session{}}
	defer r.close()
	return r.readDoc(p, plain)
}

func TestReadOfficeDocuments(t *testing.T) {
	docx := zipDoc(t, "a.docx", map[string]string{"word/document.xml": `<?xml version="1.0"?>
<w:document xmlns:w="w"><w:body>
<w:p><w:r><w:t>Invoice</w:t></w:r><w:r><w:tab/><w:t xml:space="preserve">42 </w:t></w:r></w:p>
<w:p><w:r><w:t>Total &amp; tax</w:t><w:br/><w:t>due</w:t></w:r></w:p>
</w:body></w:document>`})
	odt := zipDoc(t, "a.odt", map[string]string{"content.xml": `<?xml version="1.0"?>
<office:document-content xmlns:office="o" xmlns:text="t"><office:body><office:text>
<text:h>Title</text:h><text:p>one<text:s text:c="3"/>two <text:span>three</text:span></text:p>
</office:text></office:body></office:document-content>`})
	pptx := zipDoc(t, "a.pptx", map[string]string{
		"ppt/slides/slide10.xml": `<p:sld xmlns:a="a" xmlns:p="p"><a:p><a:r><a:t>last</a:t></a:r></a:p></p:sld>`,
		"ppt/slides/slide2.xml":  `<p:sld xmlns:a="a" xmlns:p="p"><a:p><a:r><a:t>first</a:t></a:r></a:p></p:sld>`,
		"ppt/notesSlides/n1.xml": `<p:notes xmlns:a="a" xmlns:p="p"><a:t>speaker notes</a:t></p:notes>`,
	})
	e := env(&fakeBackend{})
	for _, c := range []struct{ path, want string }{
		{docx, "Invoice\t42 \nTotal & tax\ndue"},
		{odt, "Title\none   two three"},
		{pptx, "first\n\nlast"},
	} {
		got, err := readWith(t, e, c.path, false)
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", filepath.Base(c.path), got, c.want)
		}
	}
	if _, err := readWith(t, e, zipDoc(t, "b.docx", map[string]string{"x.xml": "<a/>"}), false); err == nil || !strings.Contains(err.Error(), "word/document.xml") {
		t.Errorf("a docx with no document part: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "c.docx")
	os.WriteFile(bad, []byte("not a zip"), 0o644)
	if _, err := readWith(t, e, bad, false); err == nil || !strings.Contains(err.Error(), "not a readable document") {
		t.Errorf("a docx that is not a zip: %v", err)
	}
}

func TestHTMLIsTextOnlyWhenAsked(t *testing.T) {
	page := `<html><head><title>x</title><style>p{}</style></head><body>
<script>alert(1)</script><h1>News</h1><p>Fish &amp; chips<br>today</p><!-- hidden --><ul><li>a</li><li>b</li></ul></body></html>`
	p := filepath.Join(t.TempDir(), "p.html")
	os.WriteFile(p, []byte(page), 0o644)
	e := env(&fakeBackend{})
	got, err := readWith(t, e, p, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := "News\n\nFish & chips\ntoday\n\na\n\nb"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Source is source: refactor and doc see the markup.
	if raw, _ := readWith(t, e, p, false); raw != page {
		t.Errorf("without plain the page was changed: %q", raw)
	}
}

func TestReadPDF(t *testing.T) {
	dir := t.TempDir()
	pdf := filepath.Join(dir, "a.pdf")
	os.WriteFile(pdf, []byte("%PDF-1.4"), 0o644)
	withText := scripts(t, map[string]string{"pdftotext": `echo "page one text"`})
	if got, err := readWith(t, env(&fakeBackend{}).withTool(withText), pdf, false); err != nil || got != "page one text" {
		t.Errorf("text layer: %q, %v", got, err)
	}

	// A scan: no text layer, so pages are rendered and read by the vision
	// model, one call each.
	scan := scripts(t, map[string]string{
		"pdftotext": `exit 0`,
		"pdftoppm":  `for n in 1 2; do printf png > "$7-$n.png"; done`,
	})
	b := &fakeBackend{reply: func(req runtime.Request) string { return "text of " + filepath.Base(req.Images[0]) }}
	got, err := readWith(t, env(b).withTool(scan), pdf, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "text of page-1.png\n\ntext of page-2.png" {
		t.Errorf("scan: %q", got)
	}
	if len(b.prompts) != 2 || b.prompts[0].Prompt != ocrPrompt {
		t.Errorf("vision calls: %+v", b.prompts)
	}

	noRender := scripts(t, map[string]string{"pdftotext": `exit 0`})
	if _, err := readWith(t, env(&fakeBackend{}).withTool(noRender), pdf, false); err == nil || !strings.Contains(err.Error(), "pdftoppm") {
		t.Errorf("scan without pdftoppm: %v", err)
	}
	none := scripts(t, map[string]string{})
	if _, err := readWith(t, env(&fakeBackend{}).withTool(none), pdf, false); err == nil || !strings.Contains(err.Error(), "pdftotext") {
		t.Errorf("no pdftotext: %v", err)
	}
}

func (e Env) withTool(f func(string) (string, error)) Env { e.Tool = f; return e }

func TestChunk(t *testing.T) {
	if got := Chunk("short", 100); len(got) != 1 || got[0] != "short" {
		t.Errorf("short text: %q", got)
	}
	text := strings.Repeat("alpha beta gamma. ", 20) + "\n\n" + strings.Repeat("x", 250) + "\n\nend"
	chunks := Chunk(text, 100)
	var total int
	for _, c := range chunks {
		if len(c) > 100 {
			t.Errorf("chunk of %d bytes: %q", len(c), c)
		}
		total += len(strings.Fields(c))
	}
	if strings.Join(chunks, "") == "" || !strings.HasSuffix(chunks[len(chunks)-1], "end") {
		t.Errorf("chunks: %q", chunks)
	}
	// Words are not cut when there is a space to break at.
	for _, c := range chunks {
		if strings.Contains(c, "alpha") && (strings.HasPrefix(c, "lpha") || strings.HasSuffix(c, "alph")) {
			t.Errorf("a word was cut: %q", c)
		}
	}
	// Multi-byte characters are never split.
	for _, c := range Chunk(strings.Repeat("é", 300), 101) {
		if !strings.HasPrefix(c, "é") || strings.ContainsRune(c, '�') {
			t.Errorf("split a character: %q", c)
		}
	}
}

// envCtx is env with models that report a context size, so long inputs
// are split.
func envCtx(b *fakeBackend, ctxTokens int) Env {
	e := env(b)
	e.Resolve = func(_ context.Context, c string) (*registry.Model, map[string]string, error) {
		return &registry.Model{ID: "m-" + c, Context: ctxTokens}, nil, nil
	}
	return e
}

func TestSummarizeSplitsWhatDoesNotFit(t *testing.T) {
	doc := filepath.Join(t.TempDir(), "long.txt")
	para := strings.Repeat("The committee met and discussed the budget. ", 30)
	os.WriteFile(doc, []byte(strings.Repeat(para+"\n\n", 12)), 0o644) // about 16 KB
	b := &fakeBackend{reply: func(req runtime.Request) string {
		if strings.HasPrefix(req.Prompt, "These are summaries") {
			return "final summary"
		}
		return "partial"
	}}
	var streamed strings.Builder
	e := envCtx(b, 3072)
	e.Stream = func(s string) { streamed.WriteString(s) }
	res, err := runTask(t, "summarize", e, doc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "final summary" {
		t.Errorf("got %q", res.Text)
	}
	if len(b.prompts) < 3 {
		t.Fatalf("expected several parts and a combining call, got %d calls", len(b.prompts))
	}
	for _, p := range b.prompts {
		if len(p.Prompt) > 3*3072 {
			t.Errorf("a prompt of %d bytes does not fit a 3072-token context", len(p.Prompt))
		}
	}
	last := b.prompts[len(b.prompts)-1].Prompt
	if !strings.Contains(last, "partial\n\n---\n\npartial") || !strings.Contains(last, "the main points") {
		t.Errorf("combining prompt: %.200q", last)
	}
	// Only the combined answer streams, not the partial summaries.
	if streamed.String() != "final summary" || !res.Streamed {
		t.Errorf("streamed %q", streamed.String())
	}

	// A document that fits is summarised in one call.
	b.prompts = nil
	if res, err := runTask(t, "summarize", envCtx(b, 8192), "a short note", "the decisions"); err != nil || res.Text != "partial" || len(b.prompts) != 1 {
		t.Errorf("short text: %q, %v, %d calls", res.Text, err, len(b.prompts))
	}
	if !strings.Contains(b.prompts[0].Prompt, "Summarize the decisions of this text") || !strings.Contains(b.prompts[0].Prompt, "a short note") {
		t.Errorf("prompt: %q", b.prompts[0].Prompt)
	}
}

func TestTranslateJoinsItsParts(t *testing.T) {
	n := 0
	b := &fakeBackend{reply: func(req runtime.Request) string { n++; return "part" + string(rune('0'+n)) }}
	var streamed strings.Builder
	e := envCtx(b, 8192)
	e.Stream = func(s string) { streamed.WriteString(s) }
	long := strings.Repeat("Une phrase en français. ", 400) // ~10 KB, over one reply
	res, err := runTask(t, "translate", e, "English", long)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.prompts) < 2 {
		t.Fatalf("a text longer than one reply went in %d calls", len(b.prompts))
	}
	want := make([]string, len(b.prompts))
	for i := range want {
		want[i] = "part" + string(rune('1'+i))
	}
	if res.Text != strings.Join(want, "\n\n") || streamed.String() != res.Text {
		t.Errorf("got %q, streamed %q", res.Text, streamed.String())
	}
	if !strings.HasPrefix(b.prompts[0].Prompt, "Translate into English:") {
		t.Errorf("prompt: %.80q", b.prompts[0].Prompt)
	}
}

func TestInputTakesAFileOrText(t *testing.T) {
	f := filepath.Join(t.TempDir(), "note.txt")
	os.WriteFile(f, []byte("from the file"), 0o644)
	b := &fakeBackend{reply: func(req runtime.Request) string { return req.Prompt }}
	e := env(b)
	for arg, want := range map[string]string{f: "from the file", "typed text": "typed text", "-": "from stdin"} {
		e.Stdin = strings.NewReader("from stdin")
		res, err := runTask(t, "summarize", e, arg)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(res.Text, want) {
			t.Errorf("%s: prompt %q", arg, res.Text)
		}
	}
}

func TestParseChecksSplit(t *testing.T) {
	for name, js := range map[string]string{
		"reduce without split": `{"tasks":[{"id":"x","params":[{"name":"p","kind":"text"}],"steps":[{"op":"generate","cap":"text","prompt":"{{p}}","reduce":"{{p}}","as":"out"}]}]}`,
		"split not in prompt":  `{"tasks":[{"id":"x","params":[{"name":"p","kind":"text"}],"steps":[{"op":"generate","cap":"text","prompt":"hi","split":"p","as":"out"}]}]}`,
		"split undefined":      `{"tasks":[{"id":"x","params":[{"name":"p","kind":"text"}],"steps":[{"op":"generate","cap":"text","prompt":"{{p}}","split":"q","as":"out"}]}]}`,
	} {
		if _, err := Parse([]byte(js)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
