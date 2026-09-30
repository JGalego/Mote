package cli

import (
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jgalego/mote/internal/motebook"
)

// embedMarkup is the Markdown that shows the file at link in a notebook: an
// image as an image, and audio and video as the HTML elements Markdown
// viewers give the controls of a player. name is the file as it was given,
// for the message when it is not one of those.
func embedMarkup(link, name string) (string, error) {
	// A path is a URL here: spaces and the like are escaped, slashes are not.
	src := (&url.URL{Path: link}).String()
	switch motebook.Kind(link) {
	case "image":
		alt := strings.TrimSpace(strings.NewReplacer("[", " ", "]", " ", "\n", " ").Replace(strings.TrimSuffix(filepath.Base(link), filepath.Ext(link))))
		return fmt.Sprintf("![%s](%s)", alt, src), nil
	case "audio":
		return fmt.Sprintf(`<audio controls src="%s"></audio>`, html.EscapeString(src)), nil
	case "video":
		return fmt.Sprintf(`<video controls src="%s"></video>`, html.EscapeString(src)), nil
	}
	return "", fmt.Errorf("%s is not an image, audio or video file mote knows how to show; add a link with /note", name)
}

// embedPath is how the notebook should refer to a file: relative to the
// notebook's folder, so that the two can move together. A session with no
// file yet refers to it from the folder mote runs in, and its links are
// written again for the folder it is saved to. It does not check that the
// file is there.
func (s *nbSession) embedPath(file string) string {
	from := "."
	if s.path != "" {
		from = filepath.Dir(s.path)
	}
	return relFrom(from, file)
}

// relFrom writes file relative to dir, as a notebook in dir refers to it.
// Folders are compared as they really are, links followed, so /var and
// /private/var are one; a file on another drive is written as it is.
func relFrom(dir, file string) string {
	rel, err := filepath.Rel(realDir(dir), filepath.Join(realDir(filepath.Dir(file)), filepath.Base(file)))
	if err != nil {
		if abs, err := filepath.Abs(file); err == nil {
			return filepath.ToSlash(abs)
		}
		return filepath.ToSlash(file)
	}
	return filepath.ToSlash(rel)
}

// realDir is a folder's absolute path with links followed, or as close to it
// as can be told.
func realDir(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

var (
	imageEmbedRe = regexp.MustCompile(`^!\[[^\]]*\]\(([^()\s]+)\)$`)
	mediaEmbedRe = regexp.MustCompile(`^<(audio|video) controls src="([^"<>]*)"></(audio|video)>$`)
)

// relinkEmbeds writes the embeds in a notebook's text again for a notebook
// that moves from one folder to another, so each still refers to its file.
// Only lines that are exactly what /embed writes are touched.
func relinkEmbeds(book *motebook.Book, from, to string) (*motebook.Book, error) {
	if realDir(from) == realDir(to) {
		return book, nil
	}
	segs := book.Segments()
	changed := false
	for i, seg := range segs {
		if seg.Cell != nil {
			continue
		}
		lines := strings.Split(seg.Prose, "\n")
		for j, line := range lines {
			var link string
			if m := imageEmbedRe.FindStringSubmatch(line); m != nil {
				link = m[1]
			} else if m := mediaEmbedRe.FindStringSubmatch(line); m != nil && m[1] == m[3] {
				link = m[2]
			} else {
				continue
			}
			path, err := url.PathUnescape(link)
			if err != nil || filepath.IsAbs(path) || strings.Contains(path, "://") {
				continue
			}
			file := filepath.Join(from, filepath.FromSlash(path))
			mark, err := embedMarkup(relFrom(to, file), path)
			if err != nil || mark == line {
				continue
			}
			lines[j], changed = mark, true
		}
		segs[i].Prose = strings.Join(lines, "\n")
	}
	if !changed {
		return book, nil
	}
	return motebook.FromSegments(segs)
}

// checkEmbeddable refuses a file that is not there, or is a directory.
func checkEmbeddable(file string) error {
	info, err := os.Stat(file)
	if err != nil {
		return fmt.Errorf("%s: no such file", file)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", file)
	}
	return nil
}
