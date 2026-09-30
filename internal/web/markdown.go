// Package web serves a notebook to a browser: its prose as HTML, its cells to
// edit and run, and the images, audio and video they refer to. It knows the
// notebook file and how to render it; running a cell is the caller's, handed
// in as a Runner, so nothing here depends on what tasks exist.
package web

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/jgalego/mote/internal/motebook"
)

// filePath is where the server hands out the files a notebook refers to.
const filePath = "/file"

// Renderer turns the Markdown prose of a notebook into HTML that is safe to
// put in a page. Raw HTML is dropped, and so are links that run script. The
// one exception is the <audio> and <video> lines mote writes for /embed,
// which are recognised by their exact form and built again from the path in
// them, so nothing else can ride in on them.
type Renderer struct {
	md goldmark.Markdown
}

// NewRenderer makes a renderer.
func NewRenderer() *Renderer {
	return &Renderer{md: goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithASTTransformers(util.Prioritized(fileLinks{}, 100))),
	)}
}

// embedRe is exactly what /embed writes for audio and video, alone on a line.
var embedRe = regexp.MustCompile(`^<(audio|video) controls src="([^"<>]*)"></(audio|video)>$`)

// Render returns the HTML for a piece of prose.
func (r *Renderer) Render(src string) (string, error) {
	// A player is set aside as a token that Markdown will leave as it is, and
	// put back once the rest is HTML. The token carries a nonce, so prose
	// cannot contain one.
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	token := "moteembed" + hex.EncodeToString(nonce[:])
	var players, originals []string
	lines := strings.Split(src, "\n")
	blank := func(i int) bool { return i < 0 || i >= len(lines) || strings.TrimSpace(lines[i]) == "" }
	var fenceChar byte
	var fenceLen int
	for i, line := range lines {
		if fenceChar != 0 {
			if motebook.Closes(line, fenceChar, fenceLen) {
				fenceChar = 0
			}
			continue // code shows what it says
		}
		if c, n, _, ok := motebook.Fence(line); ok {
			fenceChar, fenceLen = c, n
			continue
		}
		// What /embed writes is a paragraph of its own; indented, it is code,
		// and run into other lines, it is text.
		if !blank(i-1) || !blank(i+1) || len(line)-len(strings.TrimLeft(line, " ")) > 3 {
			continue
		}
		m := embedRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || m[1] != m[3] {
			continue
		}
		link, err := url.PathUnescape(m[2])
		if err != nil || !local(link) {
			continue
		}
		players = append(players, fmt.Sprintf(`<%s controls preload="metadata" src="%s"></%s>`, m[1], html.EscapeString(fileURL(link)), m[1]))
		originals = append(originals, strings.TrimSpace(line))
		lines[i] = fmt.Sprintf("%s%dx", token, len(players)-1)
	}
	var out bytes.Buffer
	if err := r.md.Convert([]byte(strings.Join(lines, "\n")), &out); err != nil {
		return "", err
	}
	result := out.String()
	for i, player := range players {
		mark := fmt.Sprintf("%s%dx", token, i)
		// Only a paragraph that is the token alone becomes the player. Where
		// Markdown put it anywhere else, in a link's address say, it goes back
		// to being the text it was.
		result = strings.Replace(result, "<p>"+mark+"</p>", player, 1)
		result = strings.ReplaceAll(result, mark, html.EscapeString(originals[i]))
	}
	return result, nil
}

// local reports whether a link is to a file beside the notebook, rather than
// to a web address, an anchor in the page or a path from the server's root.
func local(link string) bool {
	if link == "" || strings.HasPrefix(link, "#") || strings.HasPrefix(link, "/") || strings.HasPrefix(link, "\\") {
		return false
	}
	u, err := url.Parse(link)
	return err == nil && u.Scheme == "" && u.Host == "" && !strings.ContainsAny(link, "\x00")
}

// fileURL is how the page asks for a file. The path goes in a query, not in
// the URL's path, since a browser resolves ".." out of a path before asking.
func fileURL(rel string) string {
	return filePath + "?" + url.Values{"p": {rel}}.Encode()
}

// fileLinks points the local links and images of a document at the server.
type fileLinks struct{}

func (fileLinks) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var dest *[]byte
		switch n := n.(type) {
		case *ast.Image:
			dest = &n.Destination
		case *ast.Link:
			dest = &n.Destination
		default:
			return ast.WalkContinue, nil
		}
		if link, err := url.PathUnescape(string(*dest)); err == nil && local(link) {
			*dest = []byte(fileURL(link))
		}
		return ast.WalkContinue, nil
	})
}

// Media names what a file is to a page that shows it: "image", "audio",
// "video", or "" for anything else.
func Media(path string) string { return motebook.Kind(path) }
