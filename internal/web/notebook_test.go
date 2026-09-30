package web

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

const sample = "# Notes\n\nSome *text*.\n\n" +
	"```mote as=a\nchat hi\n```\n\n```output key=k\nhello\n```\n\n" +
	"Between.\n\n" +
	"```mote\nchat \"{{a}}\"\n```\n"

func decode(t *testing.T, body []byte) notebookJSON {
	t.Helper()
	var nb notebookJSON
	if err := json.Unmarshal(body, &nb); err != nil {
		t.Fatalf("not a notebook: %v\n%s", err, body)
	}
	return nb
}

func notebook(t *testing.T, st *site) notebookJSON {
	t.Helper()
	resp, body := st.get("/api/notebook")
	if resp.StatusCode != 200 {
		t.Fatalf("notebook: %d %s", resp.StatusCode, body)
	}
	return decode(t, body)
}

func TestNotebookListsProseAndCells(t *testing.T) {
	st := newSite(t, sample)
	nb := notebook(t, st)
	if nb.Name != "book.mote.md" || nb.Rev < 1 || len(nb.Segments) != 4 {
		t.Fatalf("notebook: %+v", nb)
	}
	kinds := ""
	for _, s := range nb.Segments {
		kinds += s.Type[:1]
	}
	if kinds != "pcpc" {
		t.Errorf("segments %q, want prose, cell, prose, cell", kinds)
	}
	if p := nb.Segments[0]; !strings.Contains(p.HTML, "<h1") || !strings.Contains(p.HTML, "<em>text</em>") || !strings.Contains(p.Markdown, "# Notes") {
		t.Errorf("prose: %+v", p)
	}
	a, b := nb.Segments[1], nb.Segments[3]
	if a.Number != 1 || a.Name != "a" || a.Expr != "chat hi" || a.Output == nil || a.Output.Text != "hello" {
		t.Errorf("first cell: %+v", a)
	}
	if b.Number != 2 || b.Output != nil || b.State != "new" {
		t.Errorf("second cell: %+v", b)
	}
	// The first cell has an output made for other input than it has now.
	if a.State != "stale" {
		t.Errorf("state %q: the fingerprint k is not this cell's", a.State)
	}
}

func TestNotebookStates(t *testing.T) {
	st := newSite(t, "")
	st.runner.changes["danger"] = true
	os.WriteFile(st.path, []byte("```mote as=a\nchat hi\n```\n\n```mote\ndanger\n```\n\n```mote\nbad task\n```\n"), 0o644)
	nb := notebook(t, st)
	for i, want := range []string{"new", "new", "new"} {
		if nb.Segments[i].State != want {
			t.Errorf("segment %d: %q, want %q", i, nb.Segments[i].State, want)
		}
	}
	if !nb.Segments[1].Changes || nb.Segments[0].Changes {
		t.Errorf("changes: %+v", nb.Segments)
	}
	if nb.Segments[2].Problem != `unknown task "bad"` {
		t.Errorf("problem: %q", nb.Segments[2].Problem)
	}
	run(t, st, nb.Rev, 0, false)
	nb = notebook(t, st)
	if nb.Segments[0].State != "fresh" {
		t.Errorf("after a run: %q", nb.Segments[0].State)
	}
	// Change the cell, and what it printed is not for it any more.
	resp, body := st.post("/api/edit", map[string]any{"rev": nb.Rev, "op": "set_cell", "index": 0, "name": "a", "expr": "chat changed"})
	if resp.StatusCode != 200 {
		t.Fatalf("edit: %d %s", resp.StatusCode, body)
	}
	if got := decode(t, body).Segments[0]; got.Output != nil || got.State != "new" {
		t.Errorf("after editing: %+v", got)
	}
}

func TestNotebookOfAFileThatIsNotThereYet(t *testing.T) {
	st := newSite(t, "")
	nb := notebook(t, st)
	if len(nb.Segments) != 0 || nb.Rev < 1 {
		t.Errorf("empty notebook: %+v", nb)
	}
	if _, err := os.Stat(st.path); err == nil {
		t.Error("viewing a notebook created its file")
	}
	// The first edit creates it.
	resp, body := st.post("/api/edit", map[string]any{"rev": nb.Rev, "op": "insert", "index": 0, "type": "prose", "markdown": "# New"})
	if resp.StatusCode != 200 {
		t.Fatalf("edit: %d %s", resp.StatusCode, body)
	}
	if got, _ := os.ReadFile(st.path); string(got) != "# New\n" {
		t.Errorf("file: %q", got)
	}
}

func TestNotebookNoticesEditsMadeElsewhere(t *testing.T) {
	st := newSite(t, sample)
	first := notebook(t, st)
	if again := notebook(t, st); again.Rev != first.Rev {
		t.Errorf("the revision moved with nothing changed: %d -> %d", first.Rev, again.Rev)
	}
	// Another program rewrites the file.
	time.Sleep(20 * time.Millisecond)
	os.WriteFile(st.path, []byte("# Rewritten\n\n```mote\nchat new\n```\n"), 0o644)
	future := time.Now().Add(time.Hour)
	os.Chtimes(st.path, future, future)
	second := notebook(t, st)
	if second.Rev <= first.Rev || len(second.Segments) != 2 || !strings.Contains(second.Segments[0].HTML, "Rewritten") {
		t.Errorf("the change was not picked up: %+v", second)
	}
	// A file that cannot be read is said so, and the notebook kept.
	os.WriteFile(st.path, []byte("```mote\nchat {{nope}}\n```\n"), 0o644)
	os.Chtimes(st.path, future.Add(time.Hour), future.Add(time.Hour))
	third := notebook(t, st)
	if !strings.Contains(third.Warning, "cannot be read") || len(third.Segments) != 2 {
		t.Errorf("a bad file: %+v", third)
	}
	// A file that is deleted is an empty notebook.
	os.Remove(st.path)
	if gone := notebook(t, st); len(gone.Segments) != 0 || gone.Rev <= third.Rev {
		t.Errorf("a deleted file: %+v", gone)
	}
}

func TestNotebookShowsMediaACellPrinted(t *testing.T) {
	st := newSite(t, "")
	outside := files(t, st)
	_ = outside
	abs := func(rel string) string { return st.dir + "/" + rel }
	cases := []struct {
		text    string
		media   int
		outside int
	}{
		{abs("pic.png"), 1, 0},
		{abs("pic.png") + "\n" + abs("sub/clip.mp4") + "\n", 2, 0},
		{abs("gone.png"), 0, 0},                     // it is not there
		{abs("pic.png") + "\nand a sentence", 0, 0}, // not only paths
		{abs("notes.txt"), 0, 0},
		{"/somewhere/else/x.png", 0, 1}, // a path the server would not serve
		{"", 0, 0},
	}
	for _, c := range cases {
		out := st.s.outputJSON(&motebookOutput{Text: c.text})
		if len(out.Media) != c.media || len(out.Outside) != c.outside {
			t.Errorf("%q: media %v, outside %v; want %d and %d", c.text, out.Media, out.Outside, c.media, c.outside)
		}
	}
	out := st.s.outputJSON(&motebookOutput{Text: abs("sub/clip.mp4")})
	if out.Media[0].Kind != "video" || out.Media[0].URL != "/file?p=sub%2Fclip.mp4" {
		t.Errorf("media: %+v", out.Media)
	}
}

func edit(t *testing.T, st *site, req map[string]any) (int, notebookJSON, string) {
	t.Helper()
	if _, ok := req["rev"]; !ok {
		req["rev"] = notebook(t, st).Rev
	}
	resp, body := st.post("/api/edit", req)
	var msg struct {
		Error    string
		Notebook *notebookJSON
	}
	json.Unmarshal(body, &msg)
	if resp.StatusCode == 200 {
		return resp.StatusCode, decode(t, body), ""
	}
	nb := notebookJSON{}
	if msg.Notebook != nil {
		nb = *msg.Notebook
	}
	return resp.StatusCode, nb, msg.Error
}

func TestEditsChangeTheNotebookAndSaveIt(t *testing.T) {
	st := newSite(t, "```mote as=a\nchat hi\n```\n\n```output key=k\nhello\n```\n")

	// Insert text and a cell, edit them, move them, delete them.
	steps := []struct {
		req  map[string]any
		want string // the file after
	}{
		{map[string]any{"op": "insert", "index": 0, "type": "prose", "markdown": "# Title"},
			"# Title\n\n```mote as=a\nchat hi\n```\n\n```output key=k\nhello\n```\n"},
		{map[string]any{"op": "insert", "index": 2, "type": "cell", "name": "b", "expr": "chat \"{{a}}\""},
			"# Title\n\n```mote as=a\nchat hi\n```\n\n```output key=k\nhello\n```\n\n```mote as=b\nchat \"{{a}}\"\n```\n"},
		{map[string]any{"op": "set_prose", "index": 0, "markdown": "# Renamed"},
			"# Renamed\n\n```mote as=a\nchat hi\n```\n\n```output key=k\nhello\n```\n\n```mote as=b\nchat \"{{a}}\"\n```\n"},
		{map[string]any{"op": "set_cell", "index": 2, "name": "b", "expr": "chat again {{a}}"},
			"# Renamed\n\n```mote as=a\nchat hi\n```\n\n```output key=k\nhello\n```\n\n```mote as=b\nchat again {{a}}\n```\n"},
		{map[string]any{"op": "move", "index": 0, "to": 2},
			"```mote as=a\nchat hi\n```\n\n```output key=k\nhello\n```\n\n```mote as=b\nchat again {{a}}\n```\n\n# Renamed\n"},
		{map[string]any{"op": "delete", "index": 2}, // the text, which the move put last
			"```mote as=a\nchat hi\n```\n\n```output key=k\nhello\n```\n\n```mote as=b\nchat again {{a}}\n```\n"},
	}
	for i, step := range steps {
		code, nb, msg := edit(t, st, step.req)
		if code != 200 {
			t.Fatalf("step %d %v: %d %s", i+1, step.req["op"], code, msg)
		}
		if got, _ := os.ReadFile(st.path); string(got) != step.want {
			t.Fatalf("step %d %v: file\n%s\nwant\n%s", i+1, step.req["op"], got, step.want)
		}
		if nb.Rev < 2 {
			t.Errorf("step %d: revision %d", i+1, nb.Rev)
		}
	}
	// Setting a cell to what it was keeps what it printed.
	code, nb, _ := edit(t, st, map[string]any{"op": "set_cell", "index": 0, "name": "a", "expr": "chat hi"})
	if code != 200 || nb.Segments[0].Output == nil || nb.Segments[0].Output.Text != "hello" {
		t.Errorf("an edit that changes nothing lost the output: %d %+v", code, nb.Segments[0])
	}
}

func TestEditsThatWouldBreakTheNotebookAreRefused(t *testing.T) {
	st := newSite(t, "Intro.\n\n```mote as=a\nchat hi\n```\n\n```mote\nchat \"{{a}}\"\n```\n")
	before, _ := os.ReadFile(st.path)
	cases := []struct {
		name string
		req  map[string]any
		want string
	}{
		{"deleting what is read", map[string]any{"op": "delete", "index": 1}, "not bound"},
		{"moving a reader before its binder", map[string]any{"op": "move", "index": 2, "to": 1}, "not bound"},
		{"binding a name twice", map[string]any{"op": "insert", "index": 3, "type": "cell", "name": "a", "expr": "chat x"}, "already bound"},
		{"a name that is not one", map[string]any{"op": "set_cell", "index": 1, "name": "a-b", "expr": "chat hi"}, "not a name"},
		{"an empty cell", map[string]any{"op": "set_cell", "index": 1, "name": "a", "expr": "  "}, "empty cell"},
		{"a cell that would end early", map[string]any{"op": "set_cell", "index": 1, "name": "a", "expr": "chat \"x\n```\ny\""}, "line of ```"},
		{"text that is a cell", map[string]any{"op": "set_prose", "index": 0, "markdown": "```mote\nchat x\n```"}, "```mote fence"},
		{"empty text", map[string]any{"op": "set_prose", "index": 0, "markdown": " "}, "empty text"},
		{"empty inserted text", map[string]any{"op": "insert", "index": 0, "type": "prose", "markdown": ""}, "empty text"},
		{"text edited as a cell", map[string]any{"op": "set_cell", "index": 0, "name": "", "expr": "chat x"}, "not a cell"},
		{"a cell edited as text", map[string]any{"op": "set_prose", "index": 1, "markdown": "x"}, "not text"},
		{"an index off the end", map[string]any{"op": "delete", "index": 9}, "no segment 9"},
		{"a negative index", map[string]any{"op": "set_cell", "index": -1, "name": "", "expr": "chat x"}, "not a cell"},
		{"inserting nowhere", map[string]any{"op": "insert", "index": 9, "type": "prose", "markdown": "x"}, "nowhere"},
		{"an unknown kind", map[string]any{"op": "insert", "index": 0, "type": "table"}, `cannot insert a "table"`},
		{"moving off the end", map[string]any{"op": "move", "index": 0, "to": 9}, "cannot move"},
		{"an unknown edit", map[string]any{"op": "explode"}, `unknown edit "explode"`},
	}
	for _, c := range cases {
		code, _, msg := edit(t, st, c.req)
		if code != 400 || !strings.Contains(msg, c.want) {
			t.Errorf("%s: %d %q, want 400 %q", c.name, code, msg, c.want)
		}
	}
	if after, _ := os.ReadFile(st.path); string(after) != string(before) {
		t.Errorf("a refused edit changed the file:\n%s", after)
	}
	// Not an edit at all.
	if resp, _ := st.post("/api/edit", "not an object"); resp.StatusCode != 400 {
		t.Errorf("garbage: %d", resp.StatusCode)
	}
}

func TestEditsMadeAgainstAStaleNotebookAreRefusedWithTheCurrentOne(t *testing.T) {
	st := newSite(t, sample)
	stale := notebook(t, st).Rev
	edit(t, st, map[string]any{"op": "insert", "index": 0, "type": "prose", "markdown": "first"}) // moves the revision
	code, nb, msg := edit(t, st, map[string]any{"rev": stale, "op": "insert", "index": 0, "type": "prose", "markdown": "second"})
	if code != 409 || !strings.Contains(msg, "changed") || nb.Rev <= stale || !strings.Contains(nb.Segments[0].Markdown, "first") {
		t.Errorf("stale edit: %d %q %+v", code, msg, nb)
	}
	if got, _ := os.ReadFile(st.path); strings.Contains(string(got), "second") {
		t.Error("a stale edit was applied")
	}
}

func TestAnUnreadableFileBlocksEdits(t *testing.T) {
	st := newSite(t, sample)
	rev := notebook(t, st).Rev
	os.WriteFile(st.path, []byte("```mote\nchat {{nope}}\n```\n"), 0o644)
	future := time.Now().Add(time.Hour)
	os.Chtimes(st.path, future, future)
	code, _, msg := edit(t, st, map[string]any{"rev": rev, "op": "delete", "index": 0})
	if code != 409 || !strings.Contains(msg, "cannot be read") {
		t.Errorf("%d %q", code, msg)
	}
}

func TestWatchTellsPagesAboutChangesMadeElsewhere(t *testing.T) {
	st := newSite(t, sample)
	events := st.s.events.subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { st.s.Watch(ctx, 10*time.Millisecond); close(stopped) }()
	defer func() { cancel(); <-stopped }()

	next := func(what string) event {
		t.Helper()
		select {
		case e := <-events:
			return e
		case <-time.After(3 * time.Second):
			t.Fatalf("no event for %s", what)
			return event{}
		}
	}
	// Nothing changes, so nothing is said.
	select {
	case e := <-events:
		t.Fatalf("an event with nothing changed: %+v", e)
	case <-time.After(150 * time.Millisecond):
	}

	before := notebook(t, st).Rev
	future := time.Now().Add(time.Hour)
	os.WriteFile(st.path, []byte("# Rewritten\n"), 0o644)
	os.Chtimes(st.path, future, future)
	if e := next("a rewritten file"); e.Type != "changed" || e.Rev <= before {
		t.Errorf("rewritten: %+v (was rev %d)", e, before)
	}
	// A file that cannot be read is said so once, and again when it can.
	os.WriteFile(st.path, []byte("```mote\nchat {{nope}}\n```\n"), 0o644)
	os.Chtimes(st.path, future.Add(time.Hour), future.Add(time.Hour))
	if e := next("an unreadable file"); e.Type != "warning" || !strings.Contains(e.Message, "cannot be read") {
		t.Errorf("unreadable: %+v", e)
	}
	select {
	case e := <-events:
		t.Errorf("the same warning was said again: %+v", e)
	case <-time.After(150 * time.Millisecond):
	}
	os.WriteFile(st.path, []byte("# Fixed\n"), 0o644)
	os.Chtimes(st.path, future.Add(2*time.Hour), future.Add(2*time.Hour))
	got := map[string]event{}
	for len(got) < 2 {
		e := next("a file that is readable again")
		got[e.Type] = e
	}
	if got["warning"].Message != "" || got["changed"].Rev == 0 {
		t.Errorf("fixed: %+v", got)
	}
}
