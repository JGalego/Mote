package cli

import (
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
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
// notebook when it has a file, so that the two can move together, and as it
// was typed when it does not. It does not check that the file is there.
func (s *nbSession) embedPath(file string) string {
	if s.path == "" {
		return filepath.ToSlash(filepath.Clean(file))
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return filepath.ToSlash(file)
	}
	dir, err := filepath.Abs(filepath.Dir(s.path))
	if err != nil {
		return filepath.ToSlash(file)
	}
	rel, err := filepath.Rel(dir, abs)
	if err != nil { // another drive, say
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
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
