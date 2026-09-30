package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type runResult struct {
	Ran      bool
	Approval bool
	Error    string
	Notebook *notebookJSON
}

func run(t *testing.T, st *site, rev, index int, approve bool) (int, runResult) {
	t.Helper()
	return runWith(t, st, map[string]any{"rev": rev, "index": index, "approve": approve})
}

func runWith(t *testing.T, st *site, req map[string]any) (int, runResult) {
	t.Helper()
	resp, body := st.post("/api/run", req)
	var r runResult
	json.Unmarshal(body, &r)
	return resp.StatusCode, r
}

const twoCells = "```mote as=a\nchat hi\n```\n\n```mote\nchat \"{{a}}\"\n```\n"

func TestRunKeepsWhatACellPrintedUnderIt(t *testing.T) {
	st := newSite(t, twoCells)
	nb := notebook(t, st)

	code, r := run(t, st, nb.Rev, 0, false)
	if code != 200 || !r.Ran || r.Notebook.Segments[0].Output.Text != "echo: chat hi" || r.Notebook.Segments[0].State != "fresh" {
		t.Fatalf("first cell: %d %+v", code, r)
	}
	// The second reads the first.
	code, r = run(t, st, r.Notebook.Rev, 1, false)
	if code != 200 || r.Notebook.Segments[1].Output.Text != `echo: chat "echo: chat hi"` {
		t.Fatalf("second cell: %d %+v", code, r)
	}
	file, _ := os.ReadFile(st.path)
	if strings.Count(string(file), "```output key=") != 2 || !strings.Contains(string(file), "echo: chat hi\n```") {
		t.Errorf("the file:\n%s", file)
	}
	// Change the first, run it again: what the second printed is out of date.
	nb = *r.Notebook
	code, body, _ := edit(t, st, map[string]any{"rev": nb.Rev, "op": "set_cell", "index": 0, "name": "a", "expr": "chat bye"})
	if code != 200 {
		t.Fatalf("edit: %d", code)
	}
	code, r = run(t, st, body.Rev, 0, false)
	if code != 200 || r.Notebook.Segments[0].State != "fresh" || r.Notebook.Segments[1].State != "stale" {
		t.Errorf("after rerunning the first: %+v", r.Notebook.Segments)
	}
}

func TestRunningAWholeNotebookSkipsWhatIsUnchanged(t *testing.T) {
	st := newSite(t, twoCells)
	// A pass over the notebook, the way the page's Run all does it.
	pass := func() (ran []bool) {
		rev := notebook(t, st).Rev
		for i := 0; i < 2; i++ {
			code, r := runWith(t, st, map[string]any{"rev": rev, "index": i, "unlessUnchanged": true})
			if code != 200 {
				t.Fatalf("cell %d: %d %+v", i, code, r)
			}
			rev = r.Notebook.Rev
			ran = append(ran, r.Ran)
		}
		return ran
	}
	if first := pass(); !first[0] || !first[1] {
		t.Errorf("the first pass ran %v", first)
	}
	if second := pass(); second[0] || second[1] {
		t.Errorf("the second pass ran %v: nothing had changed", second)
	}
	if got := st.runner.runs(); len(got) != 2 {
		t.Errorf("the runner was called %v", got)
	}
}

func TestRunAsksBeforeACellThatCanChangeThings(t *testing.T) {
	st := newSite(t, "```mote\ndanger\n```\n")
	st.runner.changes["danger"] = true
	rev := notebook(t, st).Rev

	code, r := run(t, st, rev, 0, false)
	if code != 409 || !r.Approval || len(st.runner.runs()) != 0 {
		t.Fatalf("without approval: %d %+v, ran %v", code, r, st.runner.runs())
	}
	code, r = run(t, st, rev, 0, true)
	if code != 200 || !r.Ran {
		t.Fatalf("approved: %d %+v", code, r)
	}
	// Running it again is the point of it, so it never counts as unchanged.
	if got := r.Notebook.Segments[0]; got.State != "always" || got.Output == nil {
		t.Errorf("cell: %+v", got)
	}
	code, r = runWith(t, st, map[string]any{"rev": r.Notebook.Rev, "index": 0, "approve": true, "unlessUnchanged": true})
	if code != 200 || !r.Ran {
		t.Errorf("skipped although it can change things: %d %+v", code, r)
	}
	// The server can be told not to ask.
	yes := newSite(t, "```mote\ndanger\n```\n", func(o *Options) { o.Approve = true })
	yes.runner.changes["danger"] = true
	if code, r := run(t, yes, notebook(t, yes).Rev, 0, false); code != 200 || !r.Ran {
		t.Errorf("--yes: %d %+v", code, r)
	}
}

func TestRunOfCellsThatMadeFilesIsNeverCalledUnchanged(t *testing.T) {
	st := newSite(t, "```mote\ndraw\n```\n")
	st.runner.files["draw"] = true
	code, r := run(t, st, notebook(t, st).Rev, 0, false)
	if code != 200 || r.Notebook.Segments[0].State != "always" {
		t.Errorf("%d %+v", code, r)
	}
}

func TestRunRefusesWhatCannotRun(t *testing.T) {
	st := newSite(t, "Text.\n\n```mote as=a\nbad task\n```\n\n```mote\nchat \"{{a}}\"\n```\n\n```mote\nfails\n```\n")
	st.runner.fail["fails"] = "the model is not installed"
	nb := notebook(t, st)
	before, _ := os.ReadFile(st.path)

	for _, c := range []struct {
		name  string
		index int
		code  int
		want  string
	}{
		{"text", 0, 400, "not a cell"},
		{"off the end", 9, 400, "not a cell"},
		{"negative", -1, 400, "not a cell"},
		{"a task that does not exist", 1, 400, `unknown task "bad"`},
		{"a name nothing has bound", 2, 400, "{{a}} has no output yet"},
		{"a cell that fails", 3, 422, "the model is not installed"},
	} {
		code, r := run(t, st, nb.Rev, c.index, true)
		if code != c.code || !strings.Contains(r.Error, c.want) {
			t.Errorf("%s: %d %q, want %d %q", c.name, code, r.Error, c.code, c.want)
		}
	}
	if len(st.runner.runs()) != 1 { // only the one that failed got as far as running
		t.Errorf("ran %v", st.runner.runs())
	}
	if after, _ := os.ReadFile(st.path); string(after) != string(before) {
		t.Errorf("a failed run changed the file:\n%s", after)
	}
	if resp, _ := st.post("/api/run", "garbage"); resp.StatusCode != 400 {
		t.Errorf("not a run: %d", resp.StatusCode)
	}
	// A failure hands back the notebook, for the page to show the error under.
	_, r := run(t, st, nb.Rev, 3, true)
	if r.Notebook == nil || r.Notebook.Rev != nb.Rev {
		t.Errorf("no notebook with the failure: %+v", r)
	}
}

func TestRunStaleRevisionIsRefused(t *testing.T) {
	st := newSite(t, twoCells)
	rev := notebook(t, st).Rev
	edit(t, st, map[string]any{"op": "insert", "index": 0, "type": "prose", "markdown": "x"})
	code, r := run(t, st, rev, 0, false)
	if code != 409 || r.Notebook == nil || len(st.runner.runs()) != 0 {
		t.Errorf("%d %+v", code, r)
	}
}

func TestRunDropsItsResultIfTheFileChangedWhileItRan(t *testing.T) {
	st := newSite(t, twoCells)
	st.runner.hook = func(ctx context.Context, expr string) {
		time.Sleep(20 * time.Millisecond)
		os.WriteFile(st.path, []byte("# Someone else wrote this\n"), 0o644)
		future := time.Now().Add(time.Hour)
		os.Chtimes(st.path, future, future)
	}
	code, r := run(t, st, notebook(t, st).Rev, 0, false)
	if code != 409 || !strings.Contains(r.Error, "changed while the cell ran") {
		t.Fatalf("%d %+v", code, r)
	}
	if got, _ := os.ReadFile(st.path); string(got) != "# Someone else wrote this\n" {
		t.Errorf("the result overwrote the other program's file:\n%s", got)
	}
}

func TestOnlyOneCellRunsAtATime(t *testing.T) {
	st := newSite(t, twoCells)
	inside, release := make(chan struct{}), make(chan struct{})
	st.runner.hook = func(ctx context.Context, expr string) {
		close(inside)
		<-release
	}
	rev := notebook(t, st).Rev
	done := make(chan int)
	go func() { code, _ := run(t, st, rev, 0, false); done <- code }()
	<-inside

	if code, r := run(t, st, rev, 1, false); code != 409 || !strings.Contains(r.Error, "already running") {
		t.Errorf("a second run: %d %+v", code, r)
	}
	code, _, msg := edit(t, st, map[string]any{"rev": rev, "op": "insert", "index": 0, "type": "prose", "markdown": "x"})
	if code != 409 || !strings.Contains(msg, "running") {
		t.Errorf("an edit while a cell runs: %d %q", code, msg)
	}
	// Reading is fine.
	if nb := notebook(t, st); len(nb.Segments) != 2 {
		t.Errorf("notebook while running: %+v", nb)
	}
	close(release)
	if code := <-done; code != 200 {
		t.Errorf("the first run: %d", code)
	}
}

func TestStoppingARequestStopsTheCell(t *testing.T) {
	st := newSite(t, twoCells)
	inside := make(chan struct{})
	sawCancel := make(chan bool, 1)
	st.runner.hook = func(ctx context.Context, expr string) {
		close(inside)
		select {
		case <-ctx.Done():
			sawCancel <- true
		case <-time.After(5 * time.Second):
			sawCancel <- false
		}
	}
	rev := notebook(t, st).Rev
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", st.ts.URL+"/api/run", strings.NewReader(`{"rev":`+itoa(rev)+`,"index":0}`))
	req.Header.Set("X-Mote", "1")
	go func() {
		if resp, err := st.client.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	<-inside
	cancel()
	if !<-sawCancel {
		t.Error("the cell went on after the page stopped waiting for it")
	}
	// And the next run is not blocked by it.
	time.Sleep(50 * time.Millisecond)
	st.runner.hook = nil
	if code, _ := run(t, st, notebook(t, st).Rev, 0, false); code != 200 {
		t.Errorf("a run after a stopped one: %d", code)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestEventsReportProgress(t *testing.T) {
	st := newSite(t, twoCells)
	req, _ := http.NewRequest("GET", st.ts.URL+"/api/events", nil)
	resp, err := st.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
	}
	events := make(chan event, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var e event
				json.Unmarshal([]byte(data), &e)
				events <- e
			}
		}
	}()
	// Wait until the stream is open before doing anything: the first thing
	// on it is the reconnect hint, which is not an event, so ask for the
	// notebook and let the subscription settle.
	time.Sleep(100 * time.Millisecond)
	run(t, st, notebook(t, st).Rev, 0, false)

	got := map[string]event{}
	deadline := time.After(3 * time.Second)
	for len(got) < 3 {
		select {
		case e := <-events:
			got[e.Type] = e
		case <-deadline:
			t.Fatalf("only got %v", got)
		}
	}
	if got["status"].Message != "thinking" || got["changed"].Rev < 2 {
		t.Errorf("events %+v", got)
	}
}

func TestBrokerNeverWaitsForAListener(t *testing.T) {
	b := newBroker()
	slow := b.subscribe()
	for i := 0; i < 100; i++ { // far more than the listener holds
		b.publish(event{Type: "status"})
	}
	if len(slow) != cap(slow) {
		t.Errorf("held %d of %d", len(slow), cap(slow))
	}
	b.unsubscribe(slow)
	b.publish(event{Type: "status"}) // nobody is listening: nothing happens
}
