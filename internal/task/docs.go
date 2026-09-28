package task

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jgalego/mote/internal/runtime"
)

const (
	maxDocXML   = 64 << 20 // largest XML part read from an office document
	maxOCRPages = 20       // pages of a scanned PDF read with the vision model
)

// readDoc returns the text of a file, extracting it from the document
// formats people keep their writing in: PDF (with pdftotext, or page images
// read by the vision model when a PDF has no text layer), Word, OpenDocument
// and PowerPoint files. With plain, HTML is reduced to the text a reader
// sees; otherwise it is source like any other. Anything else is read as
// plain text.
func (r *run) readDoc(p string, plain bool) (string, error) {
	var txt string
	var err error
	switch strings.ToLower(filepath.Ext(p)) {
	case ".pdf":
		txt, err = r.readPDF(p)
	case ".docx", ".docm":
		txt, err = officeText(p, "word/document.xml")
	case ".odt", ".odp", ".ods":
		txt, err = officeText(p, "content.xml")
	case ".pptx":
		txt, err = officeText(p, "ppt/slides/slide*.xml")
	case ".html", ".htm", ".xhtml":
		if !plain {
			return readText(p)
		}
		var b []byte
		if b, err = readLimited(p); err == nil {
			txt = HTMLText(string(b))
		}
	default:
		return readText(p)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", p, err)
	}
	txt = strings.TrimSpace(txt)
	if txt == "" {
		return "", fmt.Errorf("%s has no text", p)
	}
	if len(txt) > maxRead {
		r.logf("read: %s has more than %d KiB of text; the rest was dropped", p, maxRead>>10)
		txt = cutRunes(txt, maxRead)
	}
	return txt, nil
}

// ExtractText returns the text of a file as the read op would see it with
// plain set: documents reduced to their text, HTML to what a reader sees.
// Without ocr a PDF with no text layer is an error rather than pages sent
// to the vision model, which is too slow for indexing a folder.
func ExtractText(ctx context.Context, env Env, p string, ocr bool) (string, error) {
	r := &run{ctx: ctx, env: env, vars: map[string]Value{}, sessions: env.Sessions, noOCR: !ocr}
	if r.sessions == nil {
		r.sessions = map[string]runtime.Session{}
		defer r.close()
	}
	return r.readDoc(p, true)
}

// readLimited reads a file that must fit in memory as a prompt source.
func readLimited(p string) ([]byte, error) {
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if st.Size() > maxDocXML {
		return nil, fmt.Errorf("larger than %d MiB", maxDocXML>>20)
	}
	return os.ReadFile(p)
}

// readPDF prefers the PDF's own text layer. A scan has none, so its pages
// are rendered and read by the vision model instead.
func (r *run) readPDF(p string) (string, error) {
	pdftotext, err := r.tool("pdftotext")
	if err != nil {
		return "", err
	}
	done := r.status("reading " + filepath.Base(p) + " with pdftotext")
	out, err := runTool(pdftotext, "-enc", "UTF-8", "-layout", p, "-")
	done(err == nil)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) != "" {
		return out, nil
	}
	if r.noOCR {
		return "", errors.New("has no text layer (a scan); `mote run summarize` reads it with the vision model")
	}
	pdftoppm, err := r.tool("pdftoppm")
	if err != nil {
		return "", errors.New("has no text layer, and pdftoppm is needed to read its pages as images")
	}
	dir, err := r.tempDir()
	if err != nil {
		return "", err
	}
	done = r.status("rendering the pages of " + filepath.Base(p))
	_, err = runTool(pdftoppm, "-r", "150", "-png", "-l", strconv.Itoa(maxOCRPages), p, filepath.Join(dir, "page"))
	done(err == nil)
	if err != nil {
		return "", err
	}
	pages, _ := filepath.Glob(filepath.Join(dir, "page*.png"))
	sort.Strings(pages)
	if len(pages) == 0 {
		return "", errors.New("has no pages")
	}
	return r.ocr(pages)
}

// ocrPrompt asks for a page's text and nothing else.
const ocrPrompt = "Transcribe all the text in this image exactly as written, keeping line breaks and reading order. Reply with the text only."

// ocr reads the text of page images with the vision model, one call each.
func (r *run) ocr(pages []string) (string, error) {
	m, sess, err := r.session("vision")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i, pg := range pages {
		done := r.status(fmt.Sprintf("reading page %d of %d with %s", i+1, len(pages), m.ID))
		res, err := sess.Generate(r.ctx, runtime.Request{Prompt: ocrPrompt, Images: []string{pg}, MaxTokens: 2048})
		done(err == nil)
		if err != nil {
			return "", err
		}
		r.calls = append(r.calls, Call{Model: m.ID, Cap: "vision", Result: res})
		b.WriteString(strings.TrimSpace(res.Text) + "\n\n")
	}
	return b.String(), nil
}

// officeText extracts the text of the XML parts matching pattern inside a
// zipped office document (docx, odt, pptx), in the order of their numbers.
func officeText(p, pattern string) (string, error) {
	z, err := zip.OpenReader(p)
	if err != nil {
		return "", fmt.Errorf("not a readable document: %w", err)
	}
	defer z.Close()
	var parts []*zip.File
	for _, f := range z.File {
		if ok, _ := filepath.Match(pattern, f.Name); ok {
			parts = append(parts, f)
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("has no %s", pattern)
	}
	sort.Slice(parts, func(i, j int) bool { return partNumber(parts[i].Name) < partNumber(parts[j].Name) })
	var b strings.Builder
	for _, f := range parts {
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		err = xmlText(io.LimitReader(rc, maxDocXML), &b, pattern == "content.xml")
		rc.Close()
		if err != nil {
			return "", fmt.Errorf("%s: %w", f.Name, err)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

var digitsRe = regexp.MustCompile(`\d+`)

// partNumber orders slide2.xml before slide10.xml.
func partNumber(name string) int {
	n, _ := strconv.Atoi(digitsRe.FindString(filepath.Base(name)))
	return n
}

// xmlText writes the character data of an office XML part, turning the
// elements that mean paragraphs, line breaks and tabs into whitespace.
// Word, OpenDocument and DrawingML use different names for the same things.
// Word and PowerPoint keep text only in <t> elements; OpenDocument (odf)
// puts it straight inside paragraphs, headings and their spans.
func xmlText(r io.Reader, b *strings.Builder, odf bool) error {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	inText := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t": // w:t, a:t
				if !odf {
					inText++
				}
			case "p", "h":
				if odf {
					inText++
				}
			case "tab":
				b.WriteString("\t")
			case "br", "cr", "line-break":
				b.WriteString("\n")
			case "s": // text:s, a run of spaces in OpenDocument
				n := 1
				for _, a := range t.Attr {
					if a.Name.Local == "c" {
						if c, err := strconv.Atoi(a.Value); err == nil && c > 0 && c < 1000 {
							n = c
						}
					}
				}
				b.WriteString(strings.Repeat(" ", n))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				if !odf {
					inText--
				}
			case "p", "h": // w:p, a:p, text:p, text:h
				if odf {
					inText--
				}
				b.WriteString("\n")
			}
		case xml.CharData:
			if inText > 0 {
				b.Write(t)
			}
		}
	}
}

var (
	dropRe  = regexp.MustCompile(`(?is)<(script|style|noscript|template|svg|head)\b.*?</(script|style|noscript|template|svg|head)\s*>|<!--.*?-->`)
	blockRe = regexp.MustCompile(`(?i)</?(p|div|br|li|ul|ol|h[1-6]|tr|table|section|article|header|footer|blockquote|pre|hr|dt|dd)\b[^>]*>`)
	tagRe   = regexp.MustCompile(`<[^>]*>`)
	spaceRe = regexp.MustCompile(`[ \t\r\f\v]+`)
	blankRe = regexp.MustCompile(`\n\s*\n\s*`)
)

// HTMLText reduces a web page to its readable text: scripts, styles and
// comments go, block elements become line breaks, entities are decoded.
func HTMLText(s string) string {
	s = dropRe.ReplaceAllString(s, " ")
	s = blockRe.ReplaceAllString(s, "\n")
	s = tagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = spaceRe.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.TrimSpace(blankRe.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// cutRunes shortens s to at most n bytes without splitting a character.
func cutRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
