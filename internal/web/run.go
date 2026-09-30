package web

import (
	"encoding/json"
	"net/http"

	"github.com/jgalego/mote/internal/motebook"
)

type runReq struct {
	Rev     int  `json:"rev"`
	Index   int  `json:"index"`
	Approve bool `json:"approve"`
	// UnlessUnchanged skips a cell whose text and inputs are what its output
	// was made from, which is what running a whole notebook does.
	UnlessUnchanged bool `json:"unlessUnchanged"`
}

// run runs one cell and keeps what it printed under it.
func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	var req runReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "not a run: %v", err)
		return
	}
	if !s.running.TryLock() {
		writeError(w, http.StatusConflict, "a cell is already running")
		return
	}
	defer s.running.Unlock()

	s.mu.Lock()
	if err := s.syncLocked(); err != nil {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "%v", err)
		return
	}
	if req.Rev != s.rev {
		s.conflict(w, "the notebook changed since this page last saw it")
		s.mu.Unlock()
		return
	}
	rev := s.rev
	segs := s.book.Segments()
	if req.Index < 0 || req.Index >= len(segs) || segs[req.Index].Cell == nil {
		s.mu.Unlock()
		writeError(w, http.StatusBadRequest, "segment %d is not a cell", req.Index)
		return
	}
	cell := *segs[req.Index].Cell
	values := s.book.Values()
	s.mu.Unlock()

	inputs := map[string]string{}
	for _, ref := range cell.Refs {
		v, ok := values[ref]
		if !ok {
			writeError(w, http.StatusBadRequest, "{{%s}} has no output yet; run the cell that binds it first", ref)
			return
		}
		inputs[ref] = v
	}
	key := motebook.Key(cell, inputs)
	changes, err := s.opts.Runner.Check(cell.Expr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if req.UnlessUnchanged && cell.Output != nil && cell.Output.Key != "" && cell.Output.Key == key {
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ran": false, "notebook": s.snapshotLocked("")})
		return
	}
	if changes && !s.opts.Approve && !req.Approve {
		writeJSON(w, http.StatusConflict, map[string]any{
			"approval": true,
			"error":    "this cell can change things, so it needs your approval to run",
		})
		return
	}

	text, files, err := s.opts.Runner.Run(r.Context(), cell.Expr, values, func(msg string) {
		s.events.publish(event{Type: "status", Message: msg})
	})
	s.events.publish(event{Type: "done"})
	if err != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error(), "notebook": s.snapshotLocked("")})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Something else may have rewritten the file while the cell ran; the
	// result is for the notebook it was run in.
	if err := s.syncLocked(); err != nil || s.rev != rev {
		s.conflict(w, "the notebook changed while the cell ran, so its result was dropped")
		return
	}
	out := motebook.Output{Key: key, Text: text}
	if files || changes {
		out.Key = "" // it does not stay true, so it never counts as unchanged
	}
	segs = s.book.Segments()
	c := *segs[req.Index].Cell
	c.Output = &out
	segs[req.Index] = motebook.Segment{Cell: &c}
	book, err := motebook.FromSegments(segs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	if err := s.saveLocked(book); err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	s.events.publish(event{Type: "changed", Rev: s.rev})
	writeJSON(w, http.StatusOK, map[string]any{"ran": true, "notebook": s.snapshotLocked("")})
}
