package web

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/jgalego/mote/internal/motebook"
)

type mediaJSON struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	URL  string `json:"url"`
}

type outputJSON struct {
	Text  string      `json:"text"`
	Media []mediaJSON `json:"media,omitempty"`
	// Outside lists media a cell printed that the server may not serve, for
	// the page to say so rather than show nothing.
	Outside []string `json:"outside,omitempty"`
}

type segmentJSON struct {
	Type     string `json:"type"` // "prose" or "cell"
	Markdown string `json:"markdown,omitempty"`
	HTML     string `json:"html,omitempty"`

	Number  int         `json:"number,omitempty"` // the cell's place among the cells, from 1
	Name    string      `json:"name,omitempty"`
	Expr    string      `json:"expr,omitempty"`
	State   string      `json:"state,omitempty"` // new, fresh, stale or always
	Changes bool        `json:"changes,omitempty"`
	Problem string      `json:"problem,omitempty"`
	Output  *outputJSON `json:"output,omitempty"`
}

type notebookJSON struct {
	Name     string        `json:"name"`
	Rev      int           `json:"rev"`
	Segments []segmentJSON `json:"segments"`
	Warning  string        `json:"warning,omitempty"`
}

// snapshotLocked is the notebook as the page sees it. It needs s.mu.
func (s *Server) snapshotLocked(warning string) notebookJSON {
	values := s.book.Values()
	nb := notebookJSON{Name: filepath.Base(s.opts.Path), Rev: s.rev, Warning: warning, Segments: []segmentJSON{}}
	number := 0
	for _, seg := range s.book.Segments() {
		if seg.Cell == nil {
			out, err := s.md.Render(seg.Prose)
			if err != nil {
				out = "<p>" + html.EscapeString(seg.Prose) + "</p>"
			}
			nb.Segments = append(nb.Segments, segmentJSON{Type: "prose", Markdown: seg.Prose, HTML: out})
			continue
		}
		c := seg.Cell
		number++
		js := segmentJSON{Type: "cell", Number: number, Name: c.Name, Expr: c.Expr, State: "new"}
		if changes, err := s.opts.Runner.Check(c.Expr); err != nil {
			js.Problem = err.Error()
		} else {
			js.Changes = changes
		}
		if c.Output != nil {
			inputs := map[string]string{}
			for _, r := range c.Refs {
				inputs[r] = values[r]
			}
			switch {
			case c.Output.Key == "":
				js.State = "always"
			case c.Output.Key == motebook.Key(*c, inputs):
				js.State = "fresh"
			default:
				js.State = "stale"
			}
			js.Output = s.outputJSON(c.Output)
		}
		nb.Segments = append(nb.Segments, js)
	}
	return nb
}

// outputJSON adds to what a cell printed the media in it: when every line of
// it is the path of an image, audio or video the server can serve.
func (s *Server) outputJSON(o *motebook.Output) *outputJSON {
	out := &outputJSON{Text: o.Text}
	var paths []string
	for _, line := range strings.Split(o.Text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	if len(paths) == 0 {
		return out
	}
	for _, p := range paths {
		if motebook.Kind(p) == "" {
			return out
		}
	}
	for _, p := range paths {
		rel, ok := s.relToNotebook(p)
		if !ok {
			out.Outside = append(out.Outside, p)
			continue
		}
		inRoot, _ := s.underRoot(rel)
		if _, err := s.root.Stat(inRoot); err != nil {
			continue // it was here once; the text says where
		}
		out.Media = append(out.Media, mediaJSON{Kind: motebook.Kind(p), Path: p, URL: fileURL(rel)})
	}
	return out
}

func (s *Server) getNotebook(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	warning := ""
	if err := s.syncLocked(); err != nil {
		warning = err.Error()
	}
	writeJSON(w, http.StatusOK, s.snapshotLocked(warning))
}

// conflict answers a request made against a notebook that has since changed,
// with the notebook as it is, for the page to show instead.
func (s *Server) conflict(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusConflict, map[string]any{"error": msg, "notebook": s.snapshotLocked("")})
}

type editReq struct {
	Rev      int    `json:"rev"`
	Op       string `json:"op"` // set_prose, set_cell, insert, delete or move
	Index    int    `json:"index"`
	To       int    `json:"to"`
	Type     string `json:"type"` // for insert: prose or cell
	Markdown string `json:"markdown"`
	Name     string `json:"name"`
	Expr     string `json:"expr"`
}

// applyEdit changes a list of segments as an edit asks. The result is not yet
// known to be a valid notebook: FromSegments says.
func applyEdit(segs []motebook.Segment, e editReq) ([]motebook.Segment, error) {
	inRange := func(i, n int) bool { return i >= 0 && i < n }
	switch e.Op {
	case "set_prose":
		if !inRange(e.Index, len(segs)) || segs[e.Index].Cell != nil {
			return nil, fmt.Errorf("segment %d is not text", e.Index)
		}
		if strings.TrimSpace(e.Markdown) == "" {
			return nil, fmt.Errorf("empty text; delete it instead")
		}
		segs[e.Index] = motebook.Segment{Prose: e.Markdown}
	case "set_cell":
		if !inRange(e.Index, len(segs)) || segs[e.Index].Cell == nil {
			return nil, fmt.Errorf("segment %d is not a cell", e.Index)
		}
		c := *segs[e.Index].Cell
		expr := strings.TrimSpace(e.Expr)
		// What a cell printed belongs to the cell it printed it for.
		if c.Name != e.Name || strings.TrimSpace(c.Expr) != expr {
			c.Output = nil
		}
		c.Name, c.Expr = e.Name, e.Expr
		segs[e.Index] = motebook.Segment{Cell: &c}
	case "insert":
		if e.Index < 0 || e.Index > len(segs) {
			return nil, fmt.Errorf("nowhere to insert at %d", e.Index)
		}
		var seg motebook.Segment
		switch e.Type {
		case "prose":
			if strings.TrimSpace(e.Markdown) == "" {
				return nil, fmt.Errorf("empty text")
			}
			seg = motebook.Segment{Prose: e.Markdown}
		case "cell":
			seg = motebook.Segment{Cell: &motebook.Cell{Name: e.Name, Expr: e.Expr}}
		default:
			return nil, fmt.Errorf("cannot insert a %q", e.Type)
		}
		segs = append(segs[:e.Index], append([]motebook.Segment{seg}, segs[e.Index:]...)...)
	case "delete":
		if !inRange(e.Index, len(segs)) {
			return nil, fmt.Errorf("no segment %d", e.Index)
		}
		segs = append(segs[:e.Index], segs[e.Index+1:]...)
	case "move":
		if !inRange(e.Index, len(segs)) || !inRange(e.To, len(segs)) {
			return nil, fmt.Errorf("cannot move %d to %d", e.Index, e.To)
		}
		seg := segs[e.Index]
		segs = append(segs[:e.Index], segs[e.Index+1:]...)
		segs = append(segs[:e.To], append([]motebook.Segment{seg}, segs[e.To:]...)...)
	default:
		return nil, fmt.Errorf("unknown edit %q", e.Op)
	}
	return segs, nil
}

func (s *Server) edit(w http.ResponseWriter, r *http.Request) {
	var req editReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "not an edit: %v", err)
		return
	}
	// The notebook stays as it is while a cell runs, or the result would land
	// on something else.
	if !s.running.TryLock() {
		writeError(w, http.StatusConflict, "a cell is running; wait for it or stop it first")
		return
	}
	defer s.running.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncLocked(); err != nil {
		writeError(w, http.StatusConflict, "%v", err)
		return
	}
	if req.Rev != s.rev {
		s.conflict(w, "the notebook changed since this page last saw it")
		return
	}
	segs, err := applyEdit(s.book.Segments(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	book, err := motebook.FromSegments(segs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if err := s.saveLocked(book); err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	s.events.publish(event{Type: "changed", Rev: s.rev})
	writeJSON(w, http.StatusOK, s.snapshotLocked(""))
}
